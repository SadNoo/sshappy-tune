package manage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SadNoo/sshappy-tune/internal/tune"
)

type fakeRunner struct {
	values         map[string]string
	ignoreApplyKey string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	if name == "modprobe" && len(args) == 1 && args[0] == "tcp_bbr" {
		return "", nil
	}
	if name != "sysctl" || len(args) < 2 {
		return "", fmt.Errorf("unexpected command: %s %v", name, args)
	}
	switch args[0] {
	case "-n":
		value, ok := r.values[args[1]]
		if !ok {
			return "", fmt.Errorf("unknown sysctl %s", args[1])
		}
		return value, nil
	case "-p":
		data, err := os.ReadFile(args[1])
		if err != nil {
			return "", err
		}
		values, err := ParseSysctlConfig(string(data))
		if err != nil {
			return "", err
		}
		for key, value := range values {
			if key == r.ignoreApplyKey {
				continue
			}
			r.values[key] = value
		}
		return "", nil
	case "-w":
		key, value, ok := strings.Cut(args[1], "=")
		if !ok {
			return "", fmt.Errorf("invalid sysctl assignment")
		}
		r.values[key] = normalize(value)
		return args[1], nil
	default:
		return "", fmt.Errorf("unexpected sysctl operation %v", args)
	}
}

func TestApplyVerificationFailureAutomaticallyRollsBack(t *testing.T) {
	temp := t.TempDir()
	paths := Paths{
		SysctlFile:  filepath.Join(temp, "etc/sysctl.d/99-sshappy-tune.conf"),
		ModulesFile: filepath.Join(temp, "etc/modules-load.d/sshappy-tune-bbr.conf"),
		StateDir:    filepath.Join(temp, "var/lib/sshappy-tune"),
		LockFile:    filepath.Join(temp, "run/lock/sshappy-tune.lock"),
	}
	runner := &fakeRunner{values: map[string]string{
		"net.ipv4.tcp_available_congestion_control": "reno cubic bbr",
	}, ignoreApplyKey: "net.ipv4.tcp_fastopen"}
	expected := make(map[string]string, len(ManagedSysctls))
	original := make(map[string]string, len(ManagedSysctls))
	for i, key := range ManagedSysctls {
		original[key] = fmt.Sprintf("old-%d", i)
		expected[key] = fmt.Sprintf("new-%d", i)
		runner.values[key] = original[key]
	}
	manager := Manager{
		Runner: runner,
		Paths:  paths,
		Now:    func() time.Time { return time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC) },
		EUID:   func() int { return 0 },
	}
	plan := BuildPlan(original, tune.Recommendation{
		Input:    tune.Input{BandwidthMbps: 500, RTTMillis: 150, Role: "proxy"},
		MemoryMB: 1024,
		Sysctls:  expected,
	})
	if _, err := manager.Apply(context.Background(), plan); err == nil {
		t.Fatal("expected post-apply verification failure")
	}
	for key, value := range original {
		if runner.values[key] != value {
			t.Fatalf("%s was not automatically restored: got %q, want %q", key, runner.values[key], value)
		}
	}
	if _, err := os.Stat(paths.SysctlFile); !os.IsNotExist(err) {
		t.Fatalf("generated sysctl file should be removed after rollback, got %v", err)
	}
	if _, err := os.Stat(paths.ModulesFile); !os.IsNotExist(err) {
		t.Fatalf("generated modules file should be removed after rollback, got %v", err)
	}
}

func TestApplyAndRollback(t *testing.T) {
	temp := t.TempDir()
	paths := Paths{
		SysctlFile:  filepath.Join(temp, "etc/sysctl.d/99-sshappy-tune.conf"),
		ModulesFile: filepath.Join(temp, "etc/modules-load.d/sshappy-tune-bbr.conf"),
		StateDir:    filepath.Join(temp, "var/lib/sshappy-tune"),
		LockFile:    filepath.Join(temp, "run/lock/sshappy-tune.lock"),
	}
	if err := os.MkdirAll(filepath.Dir(paths.SysctlFile), 0o755); err != nil {
		t.Fatal(err)
	}
	const previousFile = "# previous owner content\n"
	if err := os.WriteFile(paths.SysctlFile, []byte(previousFile), 0o640); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{values: map[string]string{
		"net.ipv4.tcp_available_congestion_control": "reno cubic bbr",
	}}
	expected := make(map[string]string, len(ManagedSysctls))
	original := make(map[string]string, len(ManagedSysctls))
	for i, key := range ManagedSysctls {
		original[key] = fmt.Sprintf("old-%d", i)
		expected[key] = fmt.Sprintf("new-%d", i)
		runner.values[key] = original[key]
	}
	recommendation := tune.Recommendation{
		Input:    tune.Input{BandwidthMbps: 500, RTTMillis: 150, Role: "proxy"},
		MemoryMB: 1024,
		Sysctls:  expected,
	}
	manager := Manager{
		Runner: runner,
		Paths:  paths,
		Now:    func() time.Time { return time.Date(2026, 8, 15, 1, 2, 3, 4, time.UTC) },
		EUID:   func() int { return 0 },
	}

	result, err := manager.Apply(context.Background(), BuildPlan(original, recommendation))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verification.OK || result.SnapshotID != "20260815T010203.000000004Z" {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	for key, value := range expected {
		if runner.values[key] != value {
			t.Fatalf("%s not applied: got %q, want %q", key, runner.values[key], value)
		}
	}
	if _, err := os.Stat(paths.ModulesFile); err != nil {
		t.Fatalf("modules file was not written: %v", err)
	}
	drift, err := manager.NeedsApply(BuildPlan(expected, recommendation))
	if err != nil {
		t.Fatal(err)
	}
	if drift.Needed {
		t.Fatalf("freshly applied configuration must be idempotent: %+v", drift)
	}

	if _, err := manager.Rollback(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	for key, value := range original {
		if runner.values[key] != value {
			t.Fatalf("%s not restored: got %q, want %q", key, runner.values[key], value)
		}
	}
	data, err := os.ReadFile(paths.SysctlFile)
	if err != nil || string(data) != previousFile {
		t.Fatalf("owned file not restored: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(paths.ModulesFile); !os.IsNotExist(err) {
		t.Fatalf("new modules file should be removed, got %v", err)
	}
}

func TestValidateSnapshotRejectsUnexpectedPath(t *testing.T) {
	manager := NewManager(&fakeRunner{})
	snapshot := Snapshot{
		Sysctls: make(map[string]string, len(ManagedSysctls)),
		Files: map[string]FileSnapshot{
			manager.Paths.SysctlFile: {Path: manager.Paths.SysctlFile},
			"/etc/shadow":            {Path: "/etc/shadow"},
		},
	}
	for _, key := range ManagedSysctls {
		snapshot.Sysctls[key] = "1"
	}
	if err := manager.validateSnapshot(snapshot); err == nil {
		t.Fatal("expected unsafe snapshot rejection")
	}
}
