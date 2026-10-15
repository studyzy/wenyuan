package monitoring

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The probe reports a stall and stops when the context ends.
func TestLoopLagProbeReportsAndStops(t *testing.T) {
	var out bytes.Buffer
	probe := &LoopLagProbe{
		Interval: time.Millisecond,
		Stall:    time.Nanosecond,
		Logger:   slog.New(slog.NewTextHandler(&out, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		probe.Run(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe must stop when the context ends")
	}
	if !strings.Contains(out.String(), "event loop lag") {
		t.Fatalf("expected a stall report, got %q", out.String())
	}
}

// A non-positive interval disables the probe.
func TestLoopLagProbeDisabled(t *testing.T) {
	probe := &LoopLagProbe{Interval: 0}
	done := make(chan struct{})
	go func() { defer close(done); probe.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a disabled probe must return immediately")
	}
}
