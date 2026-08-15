package manage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SadNoo/sshappy-tune/internal/host"
	"github.com/SadNoo/sshappy-tune/internal/runx"
)

type Paths struct {
	SysctlFile  string
	ModulesFile string
	StateDir    string
	LockFile    string
}

func DefaultPaths() Paths {
	return Paths{
		SysctlFile:  "/etc/sysctl.d/99-sshappy-tune.conf",
		ModulesFile: "/etc/modules-load.d/sshappy-tune-bbr.conf",
		StateDir:    "/var/lib/sshappy-tune",
		LockFile:    "/run/lock/sshappy-tune.lock",
	}
}

type Manager struct {
	Runner runx.Runner
	Paths  Paths
	Now    func() time.Time
	EUID   func() int
}

type ApplyResult struct {
	SnapshotID   string       `json:"snapshotId"`
	Verification Verification `json:"verification"`
}

type Check struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	OK       bool   `json:"ok"`
	Critical bool   `json:"critical"`
}

type Verification struct {
	OK       bool     `json:"ok"`
	Checks   []Check  `json:"checks"`
	Warnings []string `json:"warnings,omitempty"`
}

func NewManager(runner runx.Runner) Manager {
	return Manager{Runner: runner, Paths: DefaultPaths(), Now: time.Now, EUID: os.Geteuid}
}

func (m Manager) Apply(ctx context.Context, plan Plan) (ApplyResult, error) {
	if err := m.requireReady(); err != nil {
		return ApplyResult{}, err
	}
	if m.EUID() != 0 {
		return ApplyResult{}, fmt.Errorf("apply requires root")
	}
	if err := validateManagedSysctls(plan.Recommendation.Sysctls); err != nil {
		return ApplyResult{}, err
	}
	lock, err := acquireLock(m.Paths.LockFile)
	if err != nil {
		return ApplyResult{}, err
	}
	defer releaseLock(lock)

	if _, err := m.Runner.Run(ctx, "modprobe", "tcp_bbr"); err != nil {
		return ApplyResult{}, fmt.Errorf("load tcp_bbr: %w", err)
	}
	available, err := m.Runner.Run(ctx, "sysctl", "-n", "net.ipv4.tcp_available_congestion_control")
	if err != nil || !wordPresent(available, "bbr") {
		return ApplyResult{}, fmt.Errorf("BBR is unavailable after loading tcp_bbr")
	}

	snapshot, err := m.captureSnapshot(ctx, sortedKeys(plan.Recommendation.Sysctls))
	if err != nil {
		return ApplyResult{}, err
	}
	if err := writeSnapshot(m.Paths.StateDir, snapshot); err != nil {
		return ApplyResult{}, fmt.Errorf("write rollback snapshot: %w", err)
	}

	applyErr := func() error {
		if err := atomicWrite(m.Paths.SysctlFile, []byte(plan.SysctlConfig), 0o644); err != nil {
			return err
		}
		if err := atomicWrite(m.Paths.ModulesFile, []byte("tcp_bbr\n"), 0o644); err != nil {
			return err
		}
		if _, err := m.Runner.Run(ctx, "sysctl", "-p", m.Paths.SysctlFile); err != nil {
			return err
		}
		return nil
	}()
	if applyErr != nil {
		rollbackErr := m.restoreSnapshot(ctx, snapshot)
		return ApplyResult{SnapshotID: snapshot.ID}, errors.Join(fmt.Errorf("apply failed: %w", applyErr), rollbackErr)
	}

	verification, err := m.verifyExpected(ctx, plan.Recommendation.Sysctls)
	if err != nil || !verification.OK {
		rollbackErr := m.restoreSnapshot(ctx, snapshot)
		if err == nil {
			err = fmt.Errorf("post-apply verification failed")
		}
		return ApplyResult{SnapshotID: snapshot.ID, Verification: verification}, errors.Join(err, rollbackErr)
	}
	return ApplyResult{SnapshotID: snapshot.ID, Verification: verification}, nil
}

func (m Manager) Verify(ctx context.Context, detector host.Detector) (Verification, error) {
	profile, err := detector.Detect(ctx)
	if err != nil {
		return Verification{}, err
	}
	expected := map[string]string{
		"net.core.default_qdisc":          "fq",
		"net.ipv4.tcp_congestion_control": "bbr",
		"net.ipv4.tcp_fastopen":           "3",
		"net.ipv4.tcp_moderate_rcvbuf":    "1",
		"net.ipv4.tcp_mtu_probing":        "1",
	}
	if data, readErr := os.ReadFile(m.Paths.SysctlFile); readErr == nil {
		managed, parseErr := ParseSysctlConfig(string(data))
		if parseErr != nil {
			return Verification{}, parseErr
		}
		expected = managed
	} else if !os.IsNotExist(readErr) {
		return Verification{}, readErr
	}

	verification := verifyMaps(profile.Sysctls, expected)
	if !wordPresent(strings.Join(profile.AvailableCongestionControl, " "), "bbr") {
		verification.Checks = append(verification.Checks, Check{
			Name: "BBR availability", Expected: "available", Actual: "unavailable", Critical: true,
		})
		verification.OK = false
	}
	if !profile.Qdisc.FQReady {
		verification.Warnings = append(verification.Warnings, "active qdisc is not fq or mq with fq leaves; version 0.1 does not rebuild live qdisc state")
	}
	verification.Warnings = append(verification.Warnings, profile.Warnings...)
	return verification, nil
}

func (m Manager) Rollback(ctx context.Context, id string) (Snapshot, error) {
	if err := m.requireReady(); err != nil {
		return Snapshot{}, err
	}
	if m.EUID() != 0 {
		return Snapshot{}, fmt.Errorf("rollback requires root")
	}
	lock, err := acquireLock(m.Paths.LockFile)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseLock(lock)
	snapshot, err := readSnapshot(m.Paths.StateDir, id)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, m.restoreSnapshot(ctx, snapshot)
}

func (m Manager) captureSnapshot(ctx context.Context, keys []string) (Snapshot, error) {
	now := m.Now().UTC()
	snapshot := Snapshot{
		Version:   snapshotVersion,
		ID:        now.Format("20060102T150405.000000000Z"),
		CreatedAt: now,
		Sysctls:   make(map[string]string, len(keys)),
		Files:     make(map[string]FileSnapshot, 2),
	}
	for _, key := range keys {
		value, err := m.Runner.Run(ctx, "sysctl", "-n", key)
		if err != nil {
			return Snapshot{}, fmt.Errorf("snapshot %s: %w", key, err)
		}
		snapshot.Sysctls[key] = normalize(value)
	}
	for _, path := range []string{m.Paths.SysctlFile, m.Paths.ModulesFile} {
		file, err := captureFile(path)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Files[path] = file
	}
	return snapshot, nil
}

func (m Manager) restoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	if err := m.validateSnapshot(snapshot); err != nil {
		return err
	}
	var errs []error
	paths := sortedKeys(snapshot.Files)
	for _, path := range paths {
		if err := restoreFile(snapshot.Files[path]); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", path, err))
		}
	}
	for _, key := range sortedKeys(snapshot.Sysctls) {
		argument := key + "=" + snapshot.Sysctls[key]
		if _, err := m.Runner.Run(ctx, "sysctl", "-w", argument); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", key, err))
		}
	}
	if len(errs) == 0 {
		verification, err := m.verifyExpected(ctx, snapshot.Sysctls)
		if err != nil {
			errs = append(errs, err)
		} else if !verification.OK {
			errs = append(errs, fmt.Errorf("restored sysctl values did not verify"))
		}
	}
	return errors.Join(errs...)
}

func (m Manager) validateSnapshot(snapshot Snapshot) error {
	if err := validateManagedSysctls(snapshot.Sysctls); err != nil {
		return fmt.Errorf("unsafe snapshot: %w", err)
	}
	if len(snapshot.Files) != 2 {
		return fmt.Errorf("unsafe snapshot: unexpected file set")
	}
	allowedFiles := map[string]struct{}{
		m.Paths.SysctlFile:  {},
		m.Paths.ModulesFile: {},
	}
	for path, file := range snapshot.Files {
		if _, ok := allowedFiles[path]; !ok || file.Path != path {
			return fmt.Errorf("unsafe snapshot: unexpected file path")
		}
	}
	return nil
}

func (m Manager) verifyExpected(ctx context.Context, expected map[string]string) (Verification, error) {
	actual := make(map[string]string, len(expected))
	for _, key := range sortedKeys(expected) {
		value, err := m.Runner.Run(ctx, "sysctl", "-n", key)
		if err != nil {
			return Verification{}, err
		}
		actual[key] = normalize(value)
	}
	return verifyMaps(actual, expected), nil
}

func verifyMaps(actual, expected map[string]string) Verification {
	verification := Verification{OK: true}
	for _, key := range sortedKeys(expected) {
		check := Check{
			Name:     key,
			Expected: normalize(expected[key]),
			Actual:   normalize(actual[key]),
			Critical: true,
		}
		check.OK = check.Expected == check.Actual
		if !check.OK {
			verification.OK = false
		}
		verification.Checks = append(verification.Checks, check)
	}
	return verification
}

func (m Manager) requireReady() error {
	if m.Runner == nil || m.Now == nil || m.EUID == nil {
		return fmt.Errorf("manager is not initialized")
	}
	if m.Paths.SysctlFile == "" || m.Paths.ModulesFile == "" || m.Paths.StateDir == "" || m.Paths.LockFile == "" {
		return fmt.Errorf("manager paths are incomplete")
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another sshappy-tune operation is running")
	}
	return file, nil
}

func releaseLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func wordPresent(values, expected string) bool {
	for _, value := range strings.Fields(values) {
		if value == expected {
			return true
		}
	}
	return false
}
