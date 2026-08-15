package automation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SadNoo/sshappy-tune/internal/tune"
)

type fakeRunner struct {
	commands []string
	failAt   string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	command := strings.Join(append([]string{name}, args...), " ")
	r.commands = append(r.commands, command)
	if command == r.failAt {
		return "", fmt.Errorf("injected failure")
	}
	if name == "systemctl" && len(args) >= 5 && args[0] == "show" {
		switch args[3] {
		case "UnitFileState":
			return "enabled", nil
		case "ActiveState":
			return "active", nil
		}
	}
	return "", nil
}

func TestInstallStatusAndUninstall(t *testing.T) {
	installer := testInstaller(t)
	profile := NewProfile(tune.Input{BandwidthMbps: 1000, RTTMillis: 150, Role: "proxy"})
	if err := installer.Install(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{installer.Paths.ProfileFile, installer.Paths.ApplyUnit, installer.Paths.VerifyUnit, installer.Paths.VerifyTimer} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected installed file %s: %v", path, err)
		}
	}
	applyData, err := os.ReadFile(installer.Paths.ApplyUnit)
	if err != nil || !strings.Contains(string(applyData), "reconcile --confirm") {
		t.Fatalf("unexpected apply unit: data=%q err=%v", applyData, err)
	}
	timerData, err := os.ReadFile(installer.Paths.VerifyTimer)
	if err != nil || !strings.Contains(string(timerData), "OnUnitActiveSec=6h") {
		t.Fatalf("unexpected verify timer: data=%q err=%v", timerData, err)
	}
	status, err := installer.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.ApplyUnitState != "enabled" || status.TimerActiveState != "active" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if err := installer.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{installer.Paths.ProfileFile, installer.Paths.ApplyUnit, installer.Paths.VerifyUnit, installer.Paths.VerifyTimer} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected removed file %s, got %v", path, err)
		}
	}
}

func TestInstallFailureRestoresOwnedFiles(t *testing.T) {
	installer := testInstaller(t)
	runner := installer.Runner.(*fakeRunner)
	runner.failAt = "systemctl enable --now sshappy-tune-verify.timer"
	const previous = "# Managed by sshappy-tune\nprevious\n"
	if err := os.MkdirAll(filepath.Dir(installer.Paths.ApplyUnit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installer.Paths.ApplyUnit, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := NewProfile(tune.Input{BandwidthMbps: 500, RTTMillis: 100, Role: "proxy"})
	if err := installer.Install(context.Background(), profile); err == nil {
		t.Fatal("expected injected installation failure")
	}
	data, err := os.ReadFile(installer.Paths.ApplyUnit)
	if err != nil || string(data) != previous {
		t.Fatalf("previous unit not restored: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(installer.Paths.ProfileFile); !os.IsNotExist(err) {
		t.Fatalf("new profile should be removed, got %v", err)
	}
}

func TestInstallRefusesForeignUnit(t *testing.T) {
	installer := testInstaller(t)
	if err := os.MkdirAll(filepath.Dir(installer.Paths.ApplyUnit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installer.Paths.ApplyUnit, []byte("[Unit]\nDescription=foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := NewProfile(tune.Input{BandwidthMbps: 500, RTTMillis: 100, Role: "proxy"})
	if err := installer.Install(context.Background(), profile); err == nil {
		t.Fatal("expected ownership validation failure")
	}
}

func TestInstallRefusesWritableBinary(t *testing.T) {
	installer := testInstaller(t)
	if err := os.Chmod(installer.Paths.BinaryFile, 0o775); err != nil {
		t.Fatal(err)
	}
	profile := NewProfile(tune.Input{BandwidthMbps: 500, RTTMillis: 100, Role: "proxy"})
	if err := installer.PreflightInstall(profile); err == nil || !strings.Contains(err.Error(), "must not be group- or world-writable") {
		t.Fatalf("expected writable binary rejection, got %v", err)
	}
}

func TestInstallRefusesUnexpectedBinaryOwner(t *testing.T) {
	installer := testInstaller(t)
	installer.BinaryOwnerUID++
	profile := NewProfile(tune.Input{BandwidthMbps: 500, RTTMillis: 100, Role: "proxy"})
	if err := installer.PreflightInstall(profile); err == nil || !strings.Contains(err.Error(), "must be owned by UID") {
		t.Fatalf("expected binary owner rejection, got %v", err)
	}
}

func TestInstallRejectsConcurrentAutomationOperation(t *testing.T) {
	installer := testInstaller(t)
	lock, err := acquireAutomationLock(installer.Paths.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAutomationLock(lock)

	profile := NewProfile(tune.Input{BandwidthMbps: 500, RTTMillis: 100, Role: "proxy"})
	err = installer.Install(context.Background(), profile)
	if err == nil || !strings.Contains(err.Error(), "another sshappy-tune automation operation is running") {
		t.Fatalf("expected concurrent operation error, got %v", err)
	}
}

func TestGeneratedSystemdUnitsValidateOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd-analyze validation runs in Linux CI")
	}
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze is unavailable")
	}
	dir := t.TempDir()
	paths := []struct {
		name string
		data string
	}{
		{"sshappy-tune-apply.service", renderApplyUnit("/bin/true")},
		{"sshappy-tune-verify.service", renderVerifyUnit("/bin/true")},
		{"sshappy-tune-verify.timer", renderVerifyTimer()},
	}
	args := []string{"verify"}
	for _, item := range paths {
		path := filepath.Join(dir, item.name)
		if err := os.WriteFile(path, []byte(item.data), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, path)
	}
	if output, err := exec.Command("systemd-analyze", args...).CombinedOutput(); err != nil {
		t.Fatalf("systemd unit validation failed: %v\n%s", err, output)
	}
}

func testInstaller(t *testing.T) Installer {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		BinaryFile:  filepath.Join(root, "usr/local/sbin/sshappy-tune"),
		ProfileFile: filepath.Join(root, "etc/sshappy-tune/profile.json"),
		ApplyUnit:   filepath.Join(root, "etc/systemd/system/sshappy-tune-apply.service"),
		VerifyUnit:  filepath.Join(root, "etc/systemd/system/sshappy-tune-verify.service"),
		VerifyTimer: filepath.Join(root, "etc/systemd/system/sshappy-tune-verify.timer"),
		LockFile:    filepath.Join(root, "run/lock/sshappy-tune-automation.lock"),
	}
	if err := os.MkdirAll(filepath.Dir(paths.BinaryFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.BinaryFile, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Installer{
		Runner: &fakeRunner{}, Paths: paths, EUID: func() int { return 0 },
		BinaryOwnerUID: uint32(os.Geteuid()),
	}
}
