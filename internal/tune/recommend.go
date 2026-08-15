package tune

import (
	"fmt"
	"math"
	"strconv"

	"github.com/SadNoo/sshappy-tune/internal/host"
)

const (
	MiB            = int64(1024 * 1024)
	minimumBuffer  = 4 * MiB
	absoluteMax    = 256 * MiB
	minimumBacklog = int64(8192)
)

type Input struct {
	BandwidthMbps int64  `json:"bandwidthMbps"`
	RTTMillis     int64  `json:"rttMillis"`
	Role          string `json:"role"`
}

type Recommendation struct {
	Input             Input             `json:"input"`
	MemoryMB          int64             `json:"memoryMB"`
	BDPBytes          int64             `json:"bdpBytes"`
	TargetBufferBytes int64             `json:"targetBufferBytes"`
	MemoryCapBytes    int64             `json:"memoryCapBytes"`
	BufferMaxBytes    int64             `json:"bufferMaxBytes"`
	CapReason         string            `json:"capReason"`
	Sysctls           map[string]string `json:"sysctls"`
	Warnings          []string          `json:"warnings,omitempty"`
}

func Recommend(profile host.Profile, input Input) (Recommendation, error) {
	if input.BandwidthMbps < 1 || input.BandwidthMbps > 100000 {
		return Recommendation{}, fmt.Errorf("bandwidth must be between 1 and 100000 Mbps")
	}
	if input.RTTMillis < 1 || input.RTTMillis > 2000 {
		return Recommendation{}, fmt.Errorf("RTT must be between 1 and 2000 ms")
	}
	if input.Role == "" {
		input.Role = "proxy"
	}
	if input.Role != "proxy" {
		return Recommendation{}, fmt.Errorf("version 0.1 supports the proxy role only")
	}
	if profile.MemoryMB < 128 {
		return Recommendation{}, fmt.Errorf("at least 128 MB of detected memory is required")
	}

	bdp := saturatingProduct(input.BandwidthMbps, input.RTTMillis, 125)
	target := saturatingAdd(saturatingProduct(bdp, 2), 2*MiB)
	if target < minimumBuffer {
		target = minimumBuffer
	}
	memoryCap := profile.MemoryMB * MiB / 32
	if memoryCap < minimumBuffer {
		memoryCap = minimumBuffer
	}
	if memoryCap > absoluteMax {
		memoryCap = absoluteMax
	}
	bufferMax := target
	reason := "2 x BDP + 2 MiB headroom"
	if bufferMax > memoryCap {
		bufferMax = memoryCap
		reason = "capped by proxy memory budget (RAM / 32)"
	}

	somaxconn := maxParsed(profile.Sysctls["net.core.somaxconn"], minimumBacklog)
	synBacklog := maxParsed(profile.Sysctls["net.ipv4.tcp_max_syn_backlog"], minimumBacklog)
	sysctls := map[string]string{
		"net.core.default_qdisc":             "fq",
		"net.core.rmem_max":                  strconv.FormatInt(bufferMax, 10),
		"net.core.somaxconn":                 strconv.FormatInt(somaxconn, 10),
		"net.core.wmem_max":                  strconv.FormatInt(bufferMax, 10),
		"net.ipv4.tcp_congestion_control":    "bbr",
		"net.ipv4.tcp_fastopen":              "3",
		"net.ipv4.tcp_max_syn_backlog":       strconv.FormatInt(synBacklog, 10),
		"net.ipv4.tcp_moderate_rcvbuf":       "1",
		"net.ipv4.tcp_mtu_probing":           "1",
		"net.ipv4.tcp_rmem":                  fmt.Sprintf("4096 87380 %d", bufferMax),
		"net.ipv4.tcp_slow_start_after_idle": "0",
		"net.ipv4.tcp_syncookies":            "1",
		"net.ipv4.tcp_wmem":                  fmt.Sprintf("4096 65536 %d", bufferMax),
	}

	recommendation := Recommendation{
		Input:             input,
		MemoryMB:          profile.MemoryMB,
		BDPBytes:          bdp,
		TargetBufferBytes: target,
		MemoryCapBytes:    memoryCap,
		BufferMaxBytes:    bufferMax,
		CapReason:         reason,
		Sysctls:           sysctls,
	}
	if profile.InterfaceSpeedMbps > 0 && input.BandwidthMbps > profile.InterfaceSpeedMbps {
		recommendation.Warnings = append(recommendation.Warnings, "requested bandwidth exceeds the reported interface speed")
	}
	if !profile.Qdisc.FQReady {
		recommendation.Warnings = append(recommendation.Warnings, "the active qdisc is not fq-ready; version 0.1 will not rebuild a live qdisc")
	}
	return recommendation, nil
}

func saturatingProduct(values ...int64) int64 {
	result := int64(1)
	for _, value := range values {
		if value > 0 && result > math.MaxInt64/value {
			return math.MaxInt64
		}
		result *= value
	}
	return result
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func maxParsed(value string, minimum int64) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum {
		return minimum
	}
	return parsed
}
