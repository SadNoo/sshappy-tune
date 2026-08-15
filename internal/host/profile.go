package host

type Qdisc struct {
	RootKind string   `json:"rootKind"`
	Leaves   []string `json:"leaves,omitempty"`
	FQReady  bool     `json:"fqReady"`
	Raw      string   `json:"raw,omitempty"`
}

type Profile struct {
	Kernel                     string            `json:"kernel"`
	Architecture               string            `json:"architecture"`
	CPUCores                   int               `json:"cpuCores"`
	MemoryMB                   int64             `json:"memoryMB"`
	DefaultInterface           string            `json:"defaultInterface"`
	InterfaceSpeedMbps         int64             `json:"interfaceSpeedMbps,omitempty"`
	TXQueues                   int               `json:"txQueues"`
	CongestionControl          string            `json:"congestionControl"`
	AvailableCongestionControl []string          `json:"availableCongestionControl"`
	Sysctls                    map[string]string `json:"sysctls"`
	Qdisc                      Qdisc             `json:"qdisc"`
	Warnings                   []string          `json:"warnings,omitempty"`
}

var InspectKeys = []string{
	"net.ipv4.tcp_congestion_control",
	"net.ipv4.tcp_available_congestion_control",
	"net.ipv4.tcp_fastopen",
	"net.ipv4.tcp_syncookies",
	"net.ipv4.tcp_mtu_probing",
	"net.ipv4.tcp_moderate_rcvbuf",
	"net.ipv4.tcp_slow_start_after_idle",
	"net.ipv4.tcp_rmem",
	"net.ipv4.tcp_wmem",
	"net.ipv4.tcp_max_syn_backlog",
	"net.core.default_qdisc",
	"net.core.rmem_max",
	"net.core.wmem_max",
	"net.core.somaxconn",
}
