package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr)
	if code := app.Run(context.Background(), []string{"version"}); code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sshappy-tune 0.2.0") {
		t.Fatalf("unexpected version output: %s", stdout.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr)
	if code := app.Run(context.Background(), []string{"unknown"}); code != 1 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unexpected error output: %s", stderr.String())
	}
}
