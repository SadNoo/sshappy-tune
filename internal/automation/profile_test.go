package automation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SadNoo/sshappy-tune/internal/tune"
)

func TestProfileRoundTrip(t *testing.T) {
	profile := NewProfile(tune.Input{BandwidthMbps: 1000, RTTMillis: 150, Role: "proxy"})
	data, err := renderProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != profile {
		t.Fatalf("got %+v, want %+v", loaded, profile)
	}
}

func TestProfileRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	data := []byte(`{"version":1,"managedBy":"sshappy-tune","input":{"bandwidthMbps":100,"rttMillis":50,"role":"proxy"}} {}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(path); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}
