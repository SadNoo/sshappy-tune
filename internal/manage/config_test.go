package manage

import (
	"strings"
	"testing"

	"github.com/SadNoo/sshappy-tune/internal/tune"
)

func TestRenderAndParseSysctl(t *testing.T) {
	recommendation := tune.Recommendation{
		Input:    tune.Input{Role: "proxy", BandwidthMbps: 500, RTTMillis: 150},
		MemoryMB: 1024,
		Sysctls: map[string]string{
			"net.ipv4.tcp_rmem":               "4096 87380 33554432",
			"net.ipv4.tcp_congestion_control": "bbr",
		},
	}
	config := RenderSysctl(recommendation)
	if strings.Index(config, "net.ipv4.tcp_congestion_control") > strings.Index(config, "net.ipv4.tcp_rmem") {
		t.Fatalf("config keys are not sorted:\n%s", config)
	}
	values, err := ParseSysctlConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if values["net.ipv4.tcp_rmem"] != "4096 87380 33554432" {
		t.Fatalf("unexpected parsed values: %#v", values)
	}
}

func TestValidateManagedSysctlsRejectsAdditionalKey(t *testing.T) {
	values := make(map[string]string, len(ManagedSysctls)+1)
	for _, key := range ManagedSysctls {
		values[key] = "1"
	}
	values["kernel.core_pattern"] = "unsafe"
	if err := validateManagedSysctls(values); err == nil {
		t.Fatal("expected allowlist validation failure")
	}
}
