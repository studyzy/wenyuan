package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		value    string
		want     Format
		wantText string
	}{
		{"pretty", FormatPretty, ""},
		{"json", FormatJSON, ""},
		{"yaml", FormatYAML, ""},
		{"JSON", FormatPretty, `invalid output format "JSON": want pretty, json, or yaml`},
		{"xml", FormatPretty, `invalid output format "xml": want pretty, json, or yaml`},
		{"", FormatPretty, `invalid output format "": want pretty, json, or yaml`},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.value)
		if tc.wantText == "" {
			if err != nil || got != tc.want {
				t.Errorf("ParseFormat(%q) = (%v, %v), want %v", tc.value, got, err, tc.want)
			}
			continue
		}
		if err == nil || err.Error() != tc.wantText {
			t.Errorf("ParseFormat(%q) error = %v, want %q", tc.value, err, tc.wantText)
		}
	}
}

type orderProbe struct {
	Zeta  string `json:"zeta" yaml:"zeta"`
	Alpha string `json:"alpha" yaml:"alpha"`
	Mid   string `json:"mid" yaml:"mid"`
}

func TestRenderJSONKeyOrderAndIndent(t *testing.T) {
	var b bytes.Buffer
	if err := FormatJSON.Render(&b, orderProbe{Zeta: "1", Alpha: "2", Mid: "3"}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"zeta\": \"1\",\n  \"alpha\": \"2\",\n  \"mid\": \"3\"\n}\n"
	if got := b.String(); got != want {
		t.Fatalf("json = %q, want %q", got, want)
	}
}

func TestRenderYAML(t *testing.T) {
	var b bytes.Buffer
	if err := FormatYAML.Render(&b, orderProbe{Zeta: "1", Alpha: "2", Mid: "3"}); err != nil {
		t.Fatal(err)
	}
	want := "zeta: \"1\"\nalpha: \"2\"\nmid: \"3\"\n"
	if got := b.String(); got != want {
		t.Fatalf("yaml = %q, want %q", got, want)
	}
}

func TestRenderPrettyRejected(t *testing.T) {
	var b bytes.Buffer
	if err := FormatPretty.Render(&b, orderProbe{}); err == nil {
		t.Fatal("pretty must not go through the serializer")
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"short", "hello", 5, "hello"},
		{"at limit", "hello", 6, "hello"},
		{"cut", "hello!", 5, "hello..."},
		{"empty", "", 5, ""},
		{
			// Multibyte quirk: the byte test fires even when no character
			// needs cutting, appending ... to an uncut string.
			"multibyte bytes exceed limit", strings.Repeat("中", 40), 100,
			strings.Repeat("中", 40) + "...",
		},
		{"multibyte cut", strings.Repeat("中", 120), 100, strings.Repeat("中", 100) + "..."},
		{"zero limit", "hello", 0, "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Truncate(tc.in, tc.limit); got != tc.want {
				t.Fatalf("Truncate = %q (len %d), want %q (len %d)", got, len(got), tc.want, len(tc.want))
			}
		})
	}
}

func TestStylingEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  ColorEnv
		want bool
	}{
		{"force wins without a tty", ColorEnv{ForceColor: true, ForceValue: "1"}, true},
		{"force value 0 is not forcing", ColorEnv{ForceColor: true, ForceValue: "0"}, false},
		{"force beats no_color", ColorEnv{ForceColor: true, ForceValue: "1", NoColor: true}, true},
		{"force loses to dumb term", ColorEnv{ForceColor: true, ForceValue: "1", Term: "dumb"}, false},
		{"no_color disables a tty", ColorEnv{NoColor: true, IsTTY: true}, false},
		{"tty enables", ColorEnv{IsTTY: true}, true},
		{"clicolor 0 disables a tty", ColorEnv{IsTTY: true, ColorSet: true, ColorValue: "0"}, false},
		{"clicolor 1 keeps a tty", ColorEnv{IsTTY: true, ColorSet: true, ColorValue: "1"}, true},
		{"no tty disables", ColorEnv{ColorSet: true, ColorValue: "1"}, false},
		{"dumb term disables a tty", ColorEnv{IsTTY: true, Term: "dumb"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StylingEnabled(tc.env); got != tc.want {
				t.Fatalf("StylingEnabled(%+v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestGradientHelpers(t *testing.T) {
	dim := NewUI(&bytes.Buffer{}, &bytes.Buffer{}, ColorEnv{})
	if got := dim.Dim("label"); got != "label" {
		t.Fatalf("disabled Dim = %q", got)
	}
	lit := NewUI(&bytes.Buffer{}, &bytes.Buffer{}, ColorEnv{ForceColor: true, ForceValue: "1"})
	got := lit.Dim("label")
	if !strings.HasPrefix(got, esc) || got == "label" {
		t.Fatalf("enabled Dim = %q, want SGR wrapping", got)
	}
	if n := strings.Count(got, resetCode); n != 1 {
		t.Fatalf("Dim has %d resets, want 1: %q", n, got)
	}
	rule := lit.gradient("a b")
	if n := strings.Count(rule, resetCode); n != 1 {
		t.Fatalf("gradient has %d resets, want 1", n)
	}
	if strings.Count(rule, "38;2;") != 2 { // a and b colored, the space not
		t.Fatalf("gradient colored the wrong number of characters: %q", rule)
	}
}

func TestUIStreams(t *testing.T) {
	var out, errB bytes.Buffer
	ui := NewUI(&out, &errB, ColorEnv{})
	ui.Infof("hello %s", "x")
	ui.Warningf("careful")
	ui.Errorf("boom")
	if !strings.HasPrefix(out.String(), "hello x\n") {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(out.String(), "warning: careful") {
		t.Errorf("warning missing from stdout: %q", out.String())
	}
	if got := errB.String(); !strings.HasPrefix(got, "error: boom\n") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestUIInfoColoredWhenForced(t *testing.T) {
	var out, errB bytes.Buffer
	ui := NewUI(&out, &errB, ColorEnv{ForceColor: true, ForceValue: "1"})
	ui.Infof("ok")
	if !strings.Contains(out.String(), brandStart) {
		t.Fatalf("info line not colored: %q", out.String())
	}
}

func TestUISection(t *testing.T) {
	var out, errB bytes.Buffer
	ui := NewUI(&out, &errB, ColorEnv{})
	ui.Section("Memories: b1 (showing 1-5)")
	want := "\n━━━ Memories: b1 (showing 1-5) ━━━\n\n"
	if got := out.String(); got != want {
		t.Fatalf("section = %q, want %q", got, want)
	}
}

func TestTableRender(t *testing.T) {
	var b bytes.Buffer
	tbl := NewTable("NAME", "STATUS")
	tbl.AddRow("one", "ok")
	tbl.AddRow("longer-name")
	tbl.AddRow("three", "running", "extra")
	if err := tbl.Render(&b); err != nil {
		t.Fatal(err)
	}
	want := "NAME         STATUS\n" +
		"one          ok\n" +
		"longer-name\n" +
		"three        running  extra\n"
	if got := b.String(); got != want {
		t.Fatalf("table =\n%q\nwant\n%q", got, want)
	}
}

func TestConfigHelpBlock(t *testing.T) {
	var b bytes.Buffer
	ConfigHelpBlock(&b, false)
	got := b.String()
	for _, want := range []string{
		"Configuration:",
		"  wenyuan-admin configure",
		"  wenyuan-admin configure --api-url http://your-api:18888",
		"    1. Environment variable (HINDSIGHT_API_URL) - highest priority",
		"    2. Config file (~/.wenyuan/config)",
		"    3. Default (http://localhost:18888)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("help block missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "profile") {
		t.Error("the documented help block omits the profile level")
	}
}
