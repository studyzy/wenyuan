package cli

// Pretty-mode decoration: the ASCII wordmark printed ahead of help and the
// progress line shown while an API command is in flight. Both are for a
// human at a terminal, so both share one gate — they appear only in pretty
// output mode on a terminal that can render them. Machine formats
// (json/yaml), piped streams, NO_COLOR and TERM=dumb all suppress them.
//
// The progress line goes to stderr, not stdout: stdout carries the command's
// result, which must stay clean for machine consumers even in pretty mode.
// Nothing here needs a dependency beyond the standard library.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// TTYEnv captures the environment inputs of the decoration decision.
type TTYEnv struct {
	// Pretty reports the pretty output mode.
	Pretty bool
	// IsTTY reports whether stdout is a terminal.
	IsTTY bool
	// NoColor reports NO_COLOR presence.
	NoColor bool
	// Term is the TERM value; "dumb" disables decoration.
	Term string
}

// DecorationsEnabled reports whether pretty-mode decoration may be written:
// the output mode is pretty, the stream is a terminal, NO_COLOR is unset and
// TERM is not dumb.
func (e TTYEnv) DecorationsEnabled() bool {
	return e.Pretty && e.IsTTY && !e.NoColor && e.Term != "dumb"
}

// TTYEnvForOS resolves the decoration policy against the process environment
// and stdout, the stream pretty output is written to. The check mirrors the
// styling policy so an interactive human gets both or neither.
func TTYEnvForOS(pretty bool) TTYEnv {
	_, noColor := os.LookupEnv("NO_COLOR")
	isTTY := false
	if info, err := os.Stdout.Stat(); err == nil {
		isTTY = info.Mode()&os.ModeCharDevice != 0
	}
	return TTYEnv{Pretty: pretty, IsTTY: isTTY, NoColor: noColor, Term: os.Getenv("TERM")}
}

// Wordmark is the ASCII wordmark printed ahead of help output. It is plain
// art, so it needs no color to read and never corrupts anything a machine
// consumer parses — the gate above keeps it off those streams entirely.
const Wordmark = "__      __  ___  _ __   _   _  _   _   __ _  _ __\n" +
	"\\ \\ /\\ / / / _ \\| '_ \\ | | | || | | | / _` || '_ \\\n" +
	" \\ V  V / |  __/| | | || |_| || |_| || (_| || | | |\n" +
	"  \\_/\\_/   \\___||_| |_| \\__, | \\___/  \\__,_||_| |_|"

// spinnerFrames cycle the progress line.
var spinnerFrames = [...]string{"|", "/", "-", "\\"}

// spinnerInterval is the frame period. 80 ms is a comfortable human cadence
// and means a command that finishes quickly draws at most a frame or two.
const spinnerInterval = 80 * time.Millisecond

// Spinner is a progress line on a writer — stderr for the CLI — erased
// before the command's result is printed. Use StartSpinner; the zero value
// is inert.
type Spinner struct {
	w      io.Writer
	label  string
	stop   chan struct{}
	done   chan struct{}
	active bool
}

// StartSpinner begins animating label on w when enabled, and returns a
// spinner whose Stop erases the line. When enabled is false it returns an
// inert spinner whose Stop is a no-op, so callers need no branch of their
// own.
func StartSpinner(w io.Writer, enabled bool, label string) *Spinner {
	if !enabled || w == nil {
		return &Spinner{}
	}
	s := &Spinner{
		w:      w,
		label:  label,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		active: true,
	}
	go s.run()
	return s
}

// run animates the line until Stop closes the stop channel. It draws one
// frame immediately — covering a command that finishes before the first tick
// — then one per interval, and exits on the tick after Stop.
func (s *Spinner) run() {
	defer close(s.done)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	for i := 0; ; i++ {
		s.draw(spinnerFrames[i%len(spinnerFrames)])
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

// draw writes one frame, carriage-returned over the previous one.
func (s *Spinner) draw(frame string) {
	_, _ = fmt.Fprintf(s.w, "\r%s %s", frame, s.label)
}

// Stop halts the animation and erases the line so the result prints on a
// clean line. It is safe on a disabled spinner, and it waits for the
// animation goroutine to exit — so no write follows Stop and no goroutine
// outlives the command, whether it succeeded or failed.
func (s *Spinner) Stop() {
	if !s.active {
		return
	}
	s.active = false
	close(s.stop)
	<-s.done
	_, _ = fmt.Fprintf(s.w, "\r%s\r", strings.Repeat(" ", len(s.label)+2))
}

// WithSpinner runs fn with a progress line on w when env allows, and returns
// fn's error unchanged. The line is always erased before WithSpinner
// returns, including when fn fails.
func WithSpinner(w io.Writer, env TTYEnv, label string, fn func() error) error {
	s := StartSpinner(w, env.DecorationsEnabled(), label)
	defer s.Stop()
	return fn()
}
