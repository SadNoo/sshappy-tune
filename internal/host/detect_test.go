package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseQdiscMultiQueueFQ(t *testing.T) {
	raw := `qdisc mq 0: root
qdisc fq 0: parent :2 limit 10000p
qdisc fq 0: parent :1 limit 10000p`
	qdisc := ParseQdisc(raw)
	if qdisc.RootKind != "mq" || !qdisc.FQReady || len(qdisc.Leaves) != 2 {
		t.Fatalf("unexpected qdisc: %+v", qdisc)
	}
}

func TestParseQdiscRejectsMixedLeaves(t *testing.T) {
	raw := `qdisc mq 0: root
qdisc fq 0: parent :1
qdisc fq_codel 0: parent :2`
	if qdisc := ParseQdisc(raw); qdisc.FQReady {
		t.Fatalf("mixed leaf qdisc must not be fq-ready: %+v", qdisc)
	}
}

func TestParseQdiscIgnoresIngressQdisc(t *testing.T) {
	raw := `qdisc mq 0: root
qdisc fq 0: parent :1
qdisc ingress ffff: parent ffff:fff1`
	qdisc := ParseQdisc(raw)
	if !qdisc.FQReady || len(qdisc.Leaves) != 1 || qdisc.Leaves[0] != "fq" {
		t.Fatalf("ingress qdisc must not be treated as an mq leaf: %+v", qdisc)
	}
}

func TestReadMemoryMB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte("MemTotal:       2097152 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readMemoryMB(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != 2048 {
		t.Fatalf("got %d MB, want 2048", value)
	}
}

func TestValidInterface(t *testing.T) {
	for _, value := range []string{"eth0", "enp0s3", "bond0.12"} {
		if !validInterface(value) {
			t.Fatalf("expected valid interface %q", value)
		}
	}
	for _, value := range []string{"", "../../etc/passwd", "interface-name-is-too-long"} {
		if validInterface(value) {
			t.Fatalf("expected invalid interface %q", value)
		}
	}
}
