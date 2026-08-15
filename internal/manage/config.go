package manage

import (
	"bufio"
	"fmt"
	"sort"
	"strings"

	"github.com/SadNoo/sshappy-tune/internal/tune"
)

var ManagedSysctls = []string{
	"net.core.default_qdisc",
	"net.core.rmem_max",
	"net.core.somaxconn",
	"net.core.wmem_max",
	"net.ipv4.tcp_congestion_control",
	"net.ipv4.tcp_fastopen",
	"net.ipv4.tcp_max_syn_backlog",
	"net.ipv4.tcp_moderate_rcvbuf",
	"net.ipv4.tcp_mtu_probing",
	"net.ipv4.tcp_rmem",
	"net.ipv4.tcp_slow_start_after_idle",
	"net.ipv4.tcp_syncookies",
	"net.ipv4.tcp_wmem",
}

type Change struct {
	Current     string `json:"current"`
	Recommended string `json:"recommended"`
	Changed     bool   `json:"changed"`
}

type Plan struct {
	Recommendation tune.Recommendation `json:"recommendation"`
	Changes        map[string]Change   `json:"changes"`
	SysctlConfig   string              `json:"sysctlConfig"`
	Actions        []string            `json:"actions"`
	Warnings       []string            `json:"warnings,omitempty"`
}

func BuildPlan(current map[string]string, recommendation tune.Recommendation) Plan {
	changes := make(map[string]Change, len(recommendation.Sysctls))
	for key, expected := range recommendation.Sysctls {
		actual := normalize(current[key])
		expected = normalize(expected)
		changes[key] = Change{Current: actual, Recommended: expected, Changed: actual != expected}
	}
	return Plan{
		Recommendation: recommendation,
		Changes:        changes,
		SysctlConfig:   RenderSysctl(recommendation),
		Actions: []string{
			"load the tcp_bbr kernel module",
			"save managed sysctl values and owned files to a rollback snapshot",
			"write /etc/sysctl.d/99-sshappy-tune.conf",
			"write /etc/modules-load.d/sshappy-tune-bbr.conf",
			"apply only the generated sysctl file",
			"verify managed values; the live qdisc is inspected but not rebuilt",
		},
		Warnings: append([]string(nil), recommendation.Warnings...),
	}
}

func RenderSysctl(recommendation tune.Recommendation) string {
	keys := sortedKeys(recommendation.Sysctls)
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by sshappy-tune. Do not edit by hand.\n")
	fmt.Fprintf(&b, "# role=%s bandwidth=%dMbps rtt=%dms memory=%dMB\n\n",
		recommendation.Input.Role,
		recommendation.Input.BandwidthMbps,
		recommendation.Input.RTTMillis,
		recommendation.MemoryMB,
	)
	for _, key := range keys {
		fmt.Fprintf(&b, "%s = %s\n", key, recommendation.Sysctls[key])
	}
	return b.String()
}

func ParseSysctlConfig(data string) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid sysctl line %q", line)
		}
		key = strings.TrimSpace(key)
		value = normalize(value)
		if key == "" || value == "" {
			return nil, fmt.Errorf("invalid sysctl line %q", line)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalize(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func validateManagedSysctls(values map[string]string) error {
	allowed := make(map[string]struct{}, len(ManagedSysctls))
	for _, key := range ManagedSysctls {
		allowed[key] = struct{}{}
	}
	if len(values) != len(allowed) {
		return fmt.Errorf("managed sysctl set is incomplete")
	}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("sysctl %s is outside the managed allowlist", key)
		}
	}
	return nil
}
