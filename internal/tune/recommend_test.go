package tune

import (
	"testing"

	"github.com/SadNoo/sshappy-tune/internal/host"
)

func TestRecommendUsesBDPWhenMemoryAllows(t *testing.T) {
	profile := host.Profile{
		MemoryMB: 4096,
		Sysctls: map[string]string{
			"net.core.somaxconn":           "65535",
			"net.ipv4.tcp_max_syn_backlog": "8192",
		},
		Qdisc: host.Qdisc{FQReady: true},
	}
	recommendation, err := Recommend(profile, Input{BandwidthMbps: 1000, RTTMillis: 150, Role: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	const wantBDP = int64(18_750_000)
	const wantBuffer = int64(39_597_152)
	if recommendation.BDPBytes != wantBDP || recommendation.BufferMaxBytes != wantBuffer {
		t.Fatalf("unexpected recommendation: %+v", recommendation)
	}
	if recommendation.Sysctls["net.core.somaxconn"] != "65535" {
		t.Fatalf("must not reduce an existing backlog")
	}
}

func TestRecommendCapsProxyBufferByMemory(t *testing.T) {
	profile := host.Profile{MemoryMB: 1024, Sysctls: map[string]string{}, Qdisc: host.Qdisc{FQReady: true}}
	recommendation, err := Recommend(profile, Input{BandwidthMbps: 2000, RTTMillis: 200, Role: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.BufferMaxBytes != 32*MiB {
		t.Fatalf("got %d, want %d", recommendation.BufferMaxBytes, 32*MiB)
	}
	if recommendation.CapReason != "capped by proxy memory budget (RAM / 32)" {
		t.Fatalf("unexpected reason: %s", recommendation.CapReason)
	}
}

func TestRecommendValidatesInputs(t *testing.T) {
	profile := host.Profile{MemoryMB: 1024}
	for _, input := range []Input{
		{BandwidthMbps: 0, RTTMillis: 100, Role: "proxy"},
		{BandwidthMbps: 100, RTTMillis: 0, Role: "proxy"},
		{BandwidthMbps: 100, RTTMillis: 100, Role: "bulk"},
	} {
		if _, err := Recommend(profile, input); err == nil {
			t.Fatalf("expected validation error for %+v", input)
		}
	}
}
