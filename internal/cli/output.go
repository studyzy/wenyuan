package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format selects how command results are rendered.
type Format int

const (
	// FormatPretty renders human-oriented text. It is the default and is
	// hand-rolled per command; it never goes through Render.
	FormatPretty Format = iota
	// FormatJSON renders pretty-printed JSON with a 2-space indent.
	FormatJSON
	// FormatYAML renders a standard YAML document without a --- marker.
	FormatYAML
)

// ParseFormat resolves a -o/--output value.
func ParseFormat(value string) (Format, error) {
	switch value {
	case "pretty":
		return FormatPretty, nil
	case "json":
		return FormatJSON, nil
	case "yaml":
		return FormatYAML, nil
	}
	return FormatPretty, fmt.Errorf("invalid output format %q: want pretty, json, or yaml", value)
}

// String returns the flag spelling of the format.
func (f Format) String() string {
	switch f {
	case FormatJSON:
		return "json"
	case FormatYAML:
		return "yaml"
	default:
		return "pretty"
	}
}

// IsMachine reports whether the format is one of the machine formats.
func (f Format) IsMachine() bool { return f == FormatJSON || f == FormatYAML }

// Render writes v in a machine format. Struct fields serialize in
// declaration order, which is how the per-command payload types pin the
// documented key order.
func (f Format) Render(w io.Writer, v any) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case FormatYAML:
		data, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		return fmt.Errorf("pretty output is hand-rolled per command")
	}
}

// Truncate cuts s to at most limit characters and appends "..." when the
// original string's byte length exceeds the limit. The byte test fires more
// often than the character cut for multibyte text; that mismatch is
// deliberate and is pinned by tests.
func Truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	runes := []rune(s)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + "..."
}

// ColorEnv captures the environment inputs of the styling decision. The
// decision is evaluated once per process; tests inject the matrix directly.
type ColorEnv struct {
	// ForceColor reports CLICOLOR_FORCE presence; ForceValue its value.
	ForceColor bool
	ForceValue string
	// NoColor reports NO_COLOR presence.
	NoColor bool
	// ColorSet reports CLICOLOR presence; ColorValue its value.
	ColorSet   bool
	ColorValue string
	// Term is the TERM value; "dumb" disables everything.
	Term string
	// IsTTY reports whether stdout is a terminal.
	IsTTY bool
}

// StylingEnabled resolves the styling policy: a forcing CLICOLOR_FORCE wins,
// then NO_COLOR, then CLICOLOR=0 combined with the tty test, and a dumb
// terminal always wins last.
func StylingEnabled(e ColorEnv) bool {
	var enabled bool
	switch {
	case e.ForceColor && e.ForceValue != "0":
		enabled = true
	case e.NoColor:
		enabled = false
	default:
		enabled = e.IsTTY && (!e.ColorSet || e.ColorValue != "0")
	}
	return enabled && e.Term != "dumb"
}

// ColorEnvForOS resolves the policy against the process environment and
// stdout.
func ColorEnvForOS() ColorEnv {
	force, forceSet := os.LookupEnv("CLICOLOR_FORCE")
	_, noColorSet := os.LookupEnv("NO_COLOR")
	color, colorSet := os.LookupEnv("CLICOLOR")
	term := os.Getenv("TERM")
	isTTY := false
	if info, err := os.Stdout.Stat(); err == nil {
		isTTY = info.Mode()&os.ModeCharDevice != 0
	}
	return ColorEnv{
		ForceColor: forceSet, ForceValue: force,
		NoColor:  noColorSet,
		ColorSet: colorSet, ColorValue: color,
		Term: term, IsTTY: isTTY,
	}
}

// SGR fragments shared by the styled output helpers.
const (
	esc       = "\x1b["
	resetCode = esc + "0m"
	// brandStart is the brand blue #0074d9; brandEnd the #009296 the brand
	// gradient ends at. dim is plain gray.
	brandStart = "38;2;0;116;217"
	brandEnd   = "38;2;0;146;150"
	dim        = "38;2;128;128;128"
	brightRed  = "1;91"
	brightCyan = "1;96"
)

// UI bundles the output streams with the resolved styling policy.
type UI struct {
	Stdout io.Writer
	Stderr io.Writer
	Color  bool
}

// NewUI resolves the styling policy once for the whole run.
func NewUI(stdout, stderr io.Writer, env ColorEnv) *UI {
	return &UI{Stdout: stdout, Stderr: stderr, Color: StylingEnabled(env)}
}

// paint wraps s in an SGR sequence when styling is on.
func (u *UI) paint(s, code string) string {
	if !u.Color {
		return s
	}
	return esc + code + "m" + s + resetCode
}

// Infof writes a success/info line to stdout in the brand color.
func (u *UI) Infof(format string, args ...any) {
	_, _ = fmt.Fprintln(u.Stdout, u.paint(fmt.Sprintf(format, args...), brandStart))
}

// Warningf writes a warning line to stdout: a colored warning: prefix and a
// plain message.
func (u *UI) Warningf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(u.Stdout, "%s %s\n", u.paint("warning:", brandEnd), msg)
}

// Errorf writes an error line to stderr: a bold bright-red error: prefix and
// a bright-red message.
func (u *UI) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(u.Stderr, "%s %s\n", u.paint("error:", brightRed), u.paint(msg, "91"))
}

// Plain writes one unstyled line to stdout.
func (u *UI) Plain(format string, args ...any) {
	_, _ = fmt.Fprintf(u.Stdout, format+"\n", args...)
}

// statusPaint tints a status word with the brand start colour when the state
// is the good one and the brand end colour otherwise. It is the reference's
// gradient_start / gradient_end split for health-style status values.
func (u *UI) statusPaint(text string, good bool) string {
	if good {
		return u.paint(text, brandStart)
	}
	return u.paint(text, brandEnd)
}

// Dim renders a dim label for pretty metadata lines.
func (u *UI) Dim(s string) string { return u.paint(s, dim) }

// Section prints a blank line, a gradient rule carrying the title, and a
// blank line.
func (u *UI) Section(title string) {
	_, _ = fmt.Fprintf(u.Stdout, "\n%s\n\n", u.gradient("━━━ "+title+" ━━━"))
}

// gradient renders s with a per-character blend from the brand start to the
// brand end color. Spaces stay uncolored and one reset closes the run.
func (u *UI) gradient(s string) string {
	if !u.Color || s == "" {
		return s
	}
	runes := []rune(s)
	colored := 0
	for _, r := range runes {
		if r != ' ' {
			colored++
		}
	}
	start, end := [3]int{0, 116, 217}, [3]int{0, 146, 150}
	var b []byte
	seen := 0
	for _, r := range runes {
		if r == ' ' {
			b = append(b, ' ')
			continue
		}
		t := 0.0
		if colored > 1 {
			t = float64(seen) / float64(colored-1)
		}
		rgb := [3]int{}
		for i := range rgb {
			rgb[i] = start[i] + int(t*float64(end[i]-start[i])+0.5)
		}
		b = append(b, []byte(fmt.Sprintf("%s38;2;%d;%d;%dm%c", esc, rgb[0], rgb[1], rgb[2], r))...)
		seen++
	}
	return string(b) + resetCode
}

// readLine reads one line from stdin and trims surrounding whitespace; an
// EOF with no data yields an empty string.
func readLine(stdin io.Reader) string {
	line, _ := readLineRaw(stdin)
	return strings.TrimSpace(line)
}

// readLineRaw reads one line without trimming, returning the error so a
// caller can distinguish EOF from an empty answer when it needs to.
func readLineRaw(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", io.EOF
	}
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			return string(line), err
		}
	}
	return string(line), nil
}

// ConfigHelpBlock writes the help shown after a configuration error. The
// priority list shows three levels and omits the profile level; the text is
// reproduced as documented rather than completed.
func ConfigHelpBlock(w io.Writer, color bool) {
	heading := "Configuration:"
	if color {
		heading = esc + brightCyan + "m" + heading + resetCode
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, heading)
	_, _ = fmt.Fprintln(w, "  Run the configure command to set the API URL:")
	_, _ = fmt.Fprintln(w, "  wenyuan-admin configure")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "  Or set it directly:")
	_, _ = fmt.Fprintln(w, "  wenyuan-admin configure --api-url http://your-api:18888")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "  Configuration priority:")
	_, _ = fmt.Fprintln(w, "    1. Environment variable (WENYUAN_API_URL) - highest priority")
	_, _ = fmt.Fprintln(w, "    2. Config file (~/.wenyuan/config)")
	_, _ = fmt.Fprintln(w, "    3. Default (http://localhost:18888)")
}
