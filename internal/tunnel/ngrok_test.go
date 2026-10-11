package tunnel

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	n := Ngrok{Domain: "name.ngrok-free.dev", Target: "127.0.0.1:4001"}
	if got := strings.Join(n.Args(), " "); !strings.HasPrefix(got, "http 127.0.0.1:4001 --url https://name.ngrok-free.dev") {
		t.Fatalf("args: %s", got)
	}
}

func TestMissingBinaryReturns(t *testing.T) {
	n := Ngrok{Path: "definitely-not-ngrok-xyz", Domain: "d", Target: "t", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n.Run(context.Background()) // must return instead of looping
}
