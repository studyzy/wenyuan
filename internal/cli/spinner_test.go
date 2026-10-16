package cli

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a writer safe for the spinner goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func TestDecorationsEnabled(t *testing.T) {
	base := TTYEnv{Pretty: true, IsTTY: true, Term: "xterm-256color"}
	cases := []struct {
		name string
		env  TTYEnv
		want bool
	}{
		{"pretty tty", base, true},
		{"json mode", TTYEnv{Pretty: false, IsTTY: true, Term: "xterm"}, false},
		{"yaml mode is not pretty", TTYEnv{Pretty: false, IsTTY: true, Term: "xterm"}, false},
		{"not a tty", TTYEnv{Pretty: true, IsTTY: false, Term: "xterm"}, false},
		{"no color", TTYEnv{Pretty: true, IsTTY: true, NoColor: true, Term: "xterm"}, false},
		{"dumb terminal", TTYEnv{Pretty: true, IsTTY: true, Term: "dumb"}, false},
		{"empty term is allowed", TTYEnv{Pretty: true, IsTTY: true}, true},
	}
	for _, tc := range cases {
		if got := tc.env.DecorationsEnabled(); got != tc.want {
			t.Errorf("%s: DecorationsEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWithSpinnerDisabledWritesNothing(t *testing.T) {
	buf := &syncBuffer{}
	ran := false
	err := WithSpinner(buf, TTYEnv{Pretty: false, IsTTY: true}, "Loading...", func() error {
		ran = true
		return nil
	})
	if !ran || err != nil {
		t.Fatalf("ran = %v, err = %v", ran, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("disabled spinner wrote %q", buf.String())
	}
}

func TestWithSpinnerNonTTYWritesNothing(t *testing.T) {
	buf := &syncBuffer{}
	if err := WithSpinner(buf, TTYEnv{Pretty: true, IsTTY: false}, "Loading...", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("non-tty spinner wrote %q", buf.String())
	}
}

func TestWithSpinnerEnabledDrawsAndErases(t *testing.T) {
	buf := &syncBuffer{}
	// Outlive several frames so the animation is actually exercised.
	err := WithSpinner(buf, TTYEnv{Pretty: true, IsTTY: true, Term: "xterm"}, "Loading...", func() error {
		time.Sleep(3 * spinnerInterval)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Loading...") {
		t.Fatalf("no spinner frame in %q", out)
	}
	// The line is erased before the result prints: the buffer ends with the
	// clear sequence (CR, spaces over the label, CR).
	cleared := "\r" + strings.Repeat(" ", len("Loading...")+2) + "\r"
	if !strings.HasSuffix(out, cleared) {
		t.Fatalf("output does not end with the erase sequence: %q", out)
	}
	// No frame may follow the erase.
	assertStops(t, buf)
}

func TestWithSpinnerStopsOnError(t *testing.T) {
	buf := &syncBuffer{}
	want := errors.New("boom")
	err := WithSpinner(buf, TTYEnv{Pretty: true, IsTTY: true, Term: "xterm"}, "Loading...", func() error {
		time.Sleep(2 * spinnerInterval)
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	cleared := "\r" + strings.Repeat(" ", len("Loading...")+2) + "\r"
	if !strings.HasSuffix(buf.String(), cleared) {
		t.Fatalf("failed command left the line dirty: %q", buf.String())
	}
	// The animation goroutine is gone: nothing writes after the erase.
	assertStops(t, buf)
}

// assertStops fails if the buffer keeps growing, which would mean the frame
// ticker goroutine outlived Stop.
func assertStops(t *testing.T, buf *syncBuffer) {
	t.Helper()
	before := buf.Len()
	time.Sleep(2 * spinnerInterval)
	if after := buf.Len(); after != before {
		t.Fatalf("spinner wrote %d bytes after Stop", after-before)
	}
}

func TestStopOnInertSpinnerIsNoOp(t *testing.T) {
	buf := &syncBuffer{}
	s := StartSpinner(buf, false, "Loading...")
	s.Stop()
	s.Stop() // idempotent for a disabled spinner
	if buf.Len() != 0 {
		t.Fatalf("inert spinner wrote %q", buf.String())
	}
}
