package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/SadNoo/sshappy-tune/internal/runx"
)

type Detector struct {
	Runner   runx.Runner
	ProcRoot string
	SysRoot  string
}

func NewDetector(runner runx.Runner) Detector {
	return Detector{Runner: runner, ProcRoot: "/proc", SysRoot: "/sys"}
}

func (d Detector) Detect(ctx context.Context) (Profile, error) {
	if runtime.GOOS != "linux" {
		return Profile{}, fmt.Errorf("sshappy-tune supports Linux only")
	}
	if d.Runner == nil {
		return Profile{}, errors.New("command runner is required")
	}

	profile := Profile{
		Architecture: runtime.GOARCH,
		CPUCores:     runtime.NumCPU(),
		Sysctls:      make(map[string]string, len(InspectKeys)),
	}
	var err error
	if profile.Kernel, err = d.Runner.Run(ctx, "uname", "-r"); err != nil {
		return Profile{}, err
	}
	if profile.MemoryMB, err = readMemoryMB(filepath.Join(d.ProcRoot, "meminfo")); err != nil {
		return Profile{}, err
	}
	if profile.DefaultInterface, err = d.defaultInterface(ctx); err != nil {
		return Profile{}, err
	}

	profile.InterfaceSpeedMbps = readPositiveInt(filepath.Join(d.SysRoot, "class/net", profile.DefaultInterface, "speed"))
	profile.TXQueues = countGlob(filepath.Join(d.SysRoot, "class/net", profile.DefaultInterface, "queues/tx-*"))
	for _, key := range InspectKeys {
		value, readErr := readSysctl(d.ProcRoot, key)
		if readErr != nil {
			profile.Warnings = append(profile.Warnings, fmt.Sprintf("cannot read %s: %v", key, readErr))
			continue
		}
		profile.Sysctls[key] = value
	}
	profile.CongestionControl = profile.Sysctls["net.ipv4.tcp_congestion_control"]
	profile.AvailableCongestionControl = strings.Fields(profile.Sysctls["net.ipv4.tcp_available_congestion_control"])

	qdiscRaw, qdiscErr := d.Runner.Run(ctx, "tc", "qdisc", "show", "dev", profile.DefaultInterface)
	if qdiscErr != nil {
		profile.Warnings = append(profile.Warnings, fmt.Sprintf("cannot inspect qdisc: %v", qdiscErr))
	} else {
		profile.Qdisc = ParseQdisc(qdiscRaw)
	}
	profile.Warnings = append(profile.Warnings, assess(profile)...)
	return profile, nil
}

func (d Detector) InspectNetworkNamespace(ctx context.Context, pid int) (map[string]string, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("sshappy-tune supports Linux only")
	}
	if pid < 1 {
		return nil, fmt.Errorf("PID must be positive")
	}
	if _, err := os.Stat(filepath.Join(d.ProcRoot, strconv.Itoa(pid), "ns/net")); err != nil {
		return nil, fmt.Errorf("network namespace for PID %d: %w", pid, err)
	}
	args := []string{"--target", strconv.Itoa(pid), "--net", "sysctl"}
	args = append(args, InspectKeys...)
	out, err := d.Runner.Run(ctx, "nsenter", args...)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(InspectKeys))
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = normalizeValue(value)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("nsenter returned no sysctl values")
	}
	return values, nil
}

func (d Detector) defaultInterface(ctx context.Context) (string, error) {
	out, err := d.Runner.Run(ctx, "ip", "-4", "route", "show", "default")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" && validInterface(fields[i+1]) {
				return fields[i+1], nil
			}
		}
	}
	return "", fmt.Errorf("default IPv4 route has no usable interface")
}

func ParseQdisc(raw string) Qdisc {
	q := Qdisc{Raw: strings.TrimSpace(raw)}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "qdisc" {
			continue
		}
		kind := fields[1]
		if contains(fields, "root") && q.RootKind == "" {
			q.RootKind = kind
		}
		if parent, ok := fieldAfter(fields, "parent"); ok && !strings.HasPrefix(parent, "ffff:") && kind != "ingress" && kind != "clsact" {
			q.Leaves = append(q.Leaves, kind)
		}
	}
	sort.Strings(q.Leaves)
	if q.RootKind == "fq" {
		q.FQReady = true
	} else if q.RootKind == "mq" && len(q.Leaves) > 0 {
		q.FQReady = true
		for _, leaf := range q.Leaves {
			if leaf != "fq" {
				q.FQReady = false
				break
			}
		}
	}
	return q
}

func readMemoryMB(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("read memory information: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr != nil || kb <= 0 {
				return 0, fmt.Errorf("invalid MemTotal value")
			}
			return kb / 1024, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal is missing")
}

func readSysctl(procRoot, key string) (string, error) {
	path := filepath.Join(procRoot, "sys", strings.ReplaceAll(key, ".", "/"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return normalizeValue(string(data)), nil
}

func normalizeValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func readPositiveInt(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func countGlob(pattern string) int {
	matches, _ := filepath.Glob(pattern)
	return len(matches)
}

func validInterface(value string) bool {
	if value == "" || len(value) > 15 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func fieldAfter(values []string, expected string) (string, bool) {
	for i := 0; i+1 < len(values); i++ {
		if values[i] == expected {
			return values[i+1], true
		}
	}
	return "", false
}

func assess(profile Profile) []string {
	var warnings []string
	if profile.CongestionControl != "bbr" {
		warnings = append(warnings, "BBR is not the active TCP congestion control")
	}
	if !contains(profile.AvailableCongestionControl, "bbr") {
		warnings = append(warnings, "BBR is not listed as an available congestion control")
	}
	if !profile.Qdisc.FQReady {
		warnings = append(warnings, "active qdisc is not fq or mq with fq leaves")
	}
	if profile.Sysctls["net.ipv4.tcp_fastopen"] != "3" {
		warnings = append(warnings, "TCP Fast Open is not enabled for both client and server")
	}
	if profile.Sysctls["net.ipv4.tcp_mtu_probing"] != "1" {
		warnings = append(warnings, "TCP MTU probing is not enabled in black-hole detection mode")
	}
	return warnings
}
