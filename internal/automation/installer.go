package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SadNoo/sshappy-tune/internal/runx"
)

const ownedMarker = "Managed by sshappy-tune"

type Paths struct {
	BinaryFile  string
	ProfileFile string
	ApplyUnit   string
	VerifyUnit  string
	VerifyTimer string
	LockFile    string
}

func DefaultPaths() Paths {
	return Paths{
		BinaryFile:  "/usr/local/sbin/sshappy-tune",
		ProfileFile: "/etc/sshappy-tune/profile.json",
		ApplyUnit:   "/etc/systemd/system/sshappy-tune-apply.service",
		VerifyUnit:  "/etc/systemd/system/sshappy-tune-verify.service",
		VerifyTimer: "/etc/systemd/system/sshappy-tune-verify.timer",
		LockFile:    "/run/lock/sshappy-tune-automation.lock",
	}
}

type Installer struct {
	Runner         runx.Runner
	Paths          Paths
	EUID           func() int
	BinaryOwnerUID uint32
}

type Status struct {
	Installed        bool    `json:"installed"`
	Profile          Profile `json:"profile"`
	ApplyUnitState   string  `json:"applyUnitState"`
	TimerUnitState   string  `json:"timerUnitState"`
	TimerActiveState string  `json:"timerActiveState"`
}

func NewInstaller(runner runx.Runner) Installer {
	return Installer{Runner: runner, Paths: DefaultPaths(), EUID: os.Geteuid, BinaryOwnerUID: 0}
}

func (i Installer) PreflightInstall(profile Profile) error {
	if err := i.ready(true); err != nil {
		return err
	}
	if _, err := renderProfile(profile); err != nil {
		return err
	}
	_, err := i.capture()
	return err
}

func (i Installer) Install(ctx context.Context, profile Profile) error {
	if err := i.ready(true); err != nil {
		return err
	}
	lock, err := acquireAutomationLock(i.Paths.LockFile)
	if err != nil {
		return err
	}
	defer releaseAutomationLock(lock)
	profileData, err := renderProfile(profile)
	if err != nil {
		return err
	}
	contents := map[string][]byte{
		i.Paths.ProfileFile: profileData,
		i.Paths.ApplyUnit:   []byte(renderApplyUnit(i.Paths.BinaryFile)),
		i.Paths.VerifyUnit:  []byte(renderVerifyUnit(i.Paths.BinaryFile)),
		i.Paths.VerifyTimer: []byte(renderVerifyTimer()),
	}
	backups, err := i.capture()
	if err != nil {
		return err
	}
	for path, data := range contents {
		if err := atomicWrite(path, data, 0o644); err != nil {
			return errors.Join(err, i.restoreInstall(ctx, backups))
		}
	}
	if _, err := i.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.Join(err, i.restoreInstall(ctx, backups))
	}
	if _, err := i.Runner.Run(ctx, "systemctl", "enable", "sshappy-tune-apply.service"); err != nil {
		return errors.Join(err, i.restoreInstall(ctx, backups))
	}
	if _, err := i.Runner.Run(ctx, "systemctl", "enable", "--now", "sshappy-tune-verify.timer"); err != nil {
		return errors.Join(err, i.restoreInstall(ctx, backups))
	}
	return nil
}

func (i Installer) Uninstall(ctx context.Context) error {
	if err := i.ready(false); err != nil {
		return err
	}
	lock, err := acquireAutomationLock(i.Paths.LockFile)
	if err != nil {
		return err
	}
	defer releaseAutomationLock(lock)
	_, timerErr := i.Runner.Run(ctx, "systemctl", "disable", "--now", "sshappy-tune-verify.timer")
	_, applyErr := i.Runner.Run(ctx, "systemctl", "disable", "sshappy-tune-apply.service")
	var errs []error
	for _, path := range []string{i.Paths.ProfileFile, i.Paths.ApplyUnit, i.Paths.VerifyUnit, i.Paths.VerifyTimer} {
		if err := removeIfOwned(path, path == i.Paths.ProfileFile); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := i.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		errs = append(errs, err)
	}
	if timerErr != nil && !isMissingUnitError(timerErr) {
		errs = append(errs, timerErr)
	}
	if applyErr != nil && !isMissingUnitError(applyErr) {
		errs = append(errs, applyErr)
	}
	return errors.Join(errs...)
}

func (i Installer) Status(ctx context.Context) (Status, error) {
	profile, err := LoadProfile(i.Paths.ProfileFile)
	if os.IsNotExist(err) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	applyState, err := i.unitProperty(ctx, "sshappy-tune-apply.service", "UnitFileState")
	if err != nil {
		return Status{}, err
	}
	timerState, err := i.unitProperty(ctx, "sshappy-tune-verify.timer", "UnitFileState")
	if err != nil {
		return Status{}, err
	}
	timerActive, err := i.unitProperty(ctx, "sshappy-tune-verify.timer", "ActiveState")
	if err != nil {
		return Status{}, err
	}
	return Status{
		Installed: true, Profile: profile, ApplyUnitState: applyState,
		TimerUnitState: timerState, TimerActiveState: timerActive,
	}, nil
}

func (i Installer) ready(requireBinary bool) error {
	if i.Runner == nil || i.EUID == nil {
		return fmt.Errorf("automation installer is not initialized")
	}
	if i.EUID() != 0 {
		return fmt.Errorf("service modification requires root")
	}
	for _, path := range []string{i.Paths.BinaryFile, i.Paths.ProfileFile, i.Paths.ApplyUnit, i.Paths.VerifyUnit, i.Paths.VerifyTimer, i.Paths.LockFile} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("automation paths must be absolute")
		}
	}
	if strings.ContainsAny(i.Paths.BinaryFile, " \t\r\n") {
		return fmt.Errorf("installed binary path must not contain whitespace")
	}
	if requireBinary {
		info, err := os.Lstat(i.Paths.BinaryFile)
		if err != nil {
			return fmt.Errorf("installed binary %s: %w", i.Paths.BinaryFile, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("installed binary must be a regular executable: %s", i.Paths.BinaryFile)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != i.BinaryOwnerUID {
			return fmt.Errorf("installed binary must be owned by UID %d: %s", i.BinaryOwnerUID, i.Paths.BinaryFile)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("installed binary must not be group- or world-writable: %s", i.Paths.BinaryFile)
		}
	}
	return nil
}

func (i Installer) capture() ([]ownedFile, error) {
	paths := []string{i.Paths.ProfileFile, i.Paths.ApplyUnit, i.Paths.VerifyUnit, i.Paths.VerifyTimer}
	files := make([]ownedFile, 0, len(paths))
	for _, path := range paths {
		isProfile := path == i.Paths.ProfileFile
		file, err := captureOwnedFile(path, func(data []byte) bool {
			if isProfile {
				var profile Profile
				return jsonUnmarshal(data, &profile) == nil && profile.ManagedBy == managedBy
			}
			return strings.Contains(string(data), "# "+ownedMarker)
		})
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func (i Installer) restoreInstall(ctx context.Context, files []ownedFile) error {
	if !ownedFileExisted(files, i.Paths.VerifyTimer) {
		_, _ = i.Runner.Run(ctx, "systemctl", "disable", "--now", "sshappy-tune-verify.timer")
	}
	if !ownedFileExisted(files, i.Paths.ApplyUnit) {
		_, _ = i.Runner.Run(ctx, "systemctl", "disable", "sshappy-tune-apply.service")
	}
	fileErr := restoreOwnedFiles(files)
	_, reloadErr := i.Runner.Run(ctx, "systemctl", "daemon-reload")
	return errors.Join(fileErr, reloadErr)
}

func ownedFileExisted(files []ownedFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return file.Exists
		}
	}
	return false
}

func (i Installer) unitProperty(ctx context.Context, unit, property string) (string, error) {
	value, err := i.Runner.Run(ctx, "systemctl", "show", unit, "--property", property, "--value")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func restoreOwnedFiles(files []ownedFile) error {
	var errs []error
	for _, file := range files {
		if err := restoreOwnedFile(file); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func removeIfOwned(path string, profile bool) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	owned := strings.Contains(string(data), "# "+ownedMarker)
	if profile {
		var parsed Profile
		owned = jsonUnmarshal(data, &parsed) == nil && parsed.ManagedBy == managedBy
	}
	if !owned {
		return fmt.Errorf("refusing to remove file not owned by sshappy-tune: %s", path)
	}
	return os.Remove(path)
}

func isMissingUnitError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not loaded") || strings.Contains(text, "does not exist") || strings.Contains(text, "not found")
}

func acquireAutomationLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another sshappy-tune automation operation is running")
	}
	return file, nil
}

func releaseAutomationLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func jsonUnmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value)
}

func renderApplyUnit(binary string) string {
	return fmt.Sprintf(`# %s
[Unit]
Description=Reconcile sshappy-tune host network profile
Wants=network-online.target
After=systemd-sysctl.service network-online.target
ConditionPathExists=/etc/sshappy-tune/profile.json

[Service]
Type=oneshot
ExecStart=%s reconcile --confirm
TimeoutStartSec=60
UMask=0077
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes

[Install]
WantedBy=multi-user.target
`, ownedMarker, binary)
}

func renderVerifyUnit(binary string) string {
	return fmt.Sprintf(`# %s
[Unit]
Description=Verify sshappy-tune host network profile
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s verify
TimeoutStartSec=45
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ProtectKernelTunables=yes
`, ownedMarker, binary)
}

func renderVerifyTimer() string {
	return fmt.Sprintf(`# %s
[Unit]
Description=Periodic sshappy-tune verification

[Timer]
OnBootSec=10min
OnUnitActiveSec=6h
RandomizedDelaySec=10min
Persistent=yes
Unit=sshappy-tune-verify.service

[Install]
WantedBy=timers.target
`, ownedMarker)
}
