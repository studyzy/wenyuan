package monitoring

import (
	"bytes"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// profileHotLoop is the known hot function the emission test drives. It is
// noinline so the CPU profiler records a frame of its own rather than folding
// it into its caller.
//
//go:noinline
func profileHotLoop(iterations int) int {
	accumulator := 0
	for i := range iterations {
		accumulator += (i * i) ^ accumulator
	}
	return accumulator
}

// The emission reports a real CPU profile: a function known to be burning CPU
// appears with its own sampled time and a sample count, under the reference's
// header wording.
func TestProfileEmitterReportsHotFunction(t *testing.T) {
	var sink int
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				sink += profileHotLoop(1 << 18)
			}
		}
	}()
	<-started

	var buffer bytes.Buffer
	emitter := &ProfileEmitter{
		Every:         1,
		Top:           20,
		Log:           slog.New(slog.NewTextHandler(&buffer, nil)),
		SampleSeconds: 1,
	}
	emitter.Emit()
	close(stop)

	output := buffer.String()
	if !strings.Contains(output, "functions active in the last 1s") {
		t.Fatalf("emission missing the reference header row:\n%s", output)
	}

	row := regexp.MustCompile(`tottime=([0-9.]+) cumtime=([0-9.]+) ncalls=([0-9]+) [^\n]*profileHotLoop`)
	match := row.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("emission has no row for the hot function:\n%s", output)
	}
	tottime, err := strconv.ParseFloat(match[1], 64)
	if err != nil || tottime <= 0 {
		t.Fatalf("hot function tottime = %q, want > 0", match[1])
	}
	ncalls, err := strconv.Atoi(match[3])
	if err != nil || ncalls < 1 {
		t.Fatalf("hot function ncalls = %q, want >= 1", match[3])
	}
	_ = sink
}
