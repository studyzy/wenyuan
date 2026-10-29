package mentalmodels

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The reference validates a trigger's cron members with croniter, whose grammar
// admits forms the standard robfig parser refuses; validCron must accept every
// form croniter accepts and keep refusing what it refuses.
func TestValidCronAcceptsCroniterForms(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"six columns with seconds", "0 0 0 * * *"},
		{"stepped seconds", "0 0 * * * */15"},
		{"seconds list", "0 0 0 * * 0,30"},
		{"day-of-week 7 is Sunday", "0 3 * * 7"},
		{"day-of-week 7 wraps a range", "0 3 * * 1-7"},
		{"day-of-week weekend wrap", "0 3 * * 5-7"},
		{"day-of-week equal bounds", "0 3 * * 7-7"},
		{"last day of month", "0 3 L * *"},
		{"lowercase last day", "0 3 l * *"},
		{"last day as a range bound", "0 3 1-l * *"},
		{"nth weekday", "0 3 * * 1#1"},
		{"nth weekday by name", "0 3 * * mon#2"},
		{"nth weekday over a range", "0 3 * * 1-5#3"},
		{"open-ended step on day-of-week", "0 3 * * 5/2"},
		{"day-of-month question mark", "0 3 ? * *"},
		{"six columns with a day question mark", "0 0 ? * * 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !validCron(tc.expr) {
				t.Fatalf("validCron(%q) = false, want true", tc.expr)
			}
		})
	}
}

// Garbage stays refused, including forms the extensions nearly reach: the
// aliases are per-column-count, the bounds still bind, and the column count
// does not grow to croniter's year field.
func TestValidCronStillRefuses(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"day-of-week past Sunday", "0 3 * * 8"},
		{"day-of-month 0 without the alias", "0 3 0 * *"},
		{"Sunday alias absent on six columns", "0 0 * * 7 *"},
		{"second past its bounds", "0 0 * * * 60"},
		{"question mark on the seconds field", "0 0 * * * ?"},
		{"seven columns", "0 3 * * * * *"},
		{"minute past its bounds", "61 0 * * *"},
		{"hour past its bounds", "0 25 * * *"},
		{"month past its bounds", "0 3 * * 13"},
		{"too few columns", "0 3 * *"},
		{"day-of-week has no last-day alias", "0 3 * * l"},
		{"last day cannot start a range", "0 3 l-5 * *"},
		{"occurrence past the fifth week", "0 3 * * 1#6"},
		{"occurrence below the first week", "0 3 * * 1#0"},
		{"occurrence must be a number", "0 3 * * 1#x"},
		{"wildcard is no weekday prefix", "0 3 * * *#1"},
		{"literal weekday mixed with nth", "0 3 * * 1,2#1"},
		{"word", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if validCron(tc.expr) {
				t.Fatalf("validCron(%q) = true, want false", tc.expr)
			}
		})
	}
}

// The widened grammar reaches the served route: a schedule the reference's
// croniter accepts is stored and echoed, and one it refuses is the same 422
// item under the member's own name.
func TestTriggerCronCroniterFormsOverHTTP(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"

	created := send(mux, http.MethodPost, base,
		`{"id":"mm-cron","name":"n","source_query":"q","trigger":{"refresh_cron":"0 3 * * 7"}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create with dow 7: %d (%s)", created.Code, created.Body.String())
	}
	got := send(mux, http.MethodGet, base+"/mm-cron", "")
	var model Model
	if err := json.Unmarshal(got.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if cron, _ := model.Trigger.Value("refresh_cron"); cron != "0 3 * * 7" {
		t.Fatalf("refresh_cron = %#v, want the stored Sunday schedule", cron)
	}

	// A patch may move the schedule to another extended form.
	if patched := send(mux, http.MethodPatch, base+"/mm-cron",
		`{"trigger":{"refresh_cron":"0 3 L * *"}}`); patched.Code != http.StatusOK {
		t.Fatalf("patch with last day: %d (%s)", patched.Code, patched.Body.String())
	}

	// Refused garbage keeps the reference's message, with the repr quoting.
	detail := decodeValidationFailure(t, send(mux, http.MethodPatch, base+"/mm-cron",
		`{"trigger":{"refresh_cron":"0 3 * * 8"}}`))
	if len(detail) != 1 {
		t.Fatalf("detail = %+v, want one item", detail)
	}
	if detail[0].Type != "value_error" || detail[0].Loc[2] != "refresh_cron" {
		t.Fatalf("item = %+v, want a value_error under refresh_cron", detail[0])
	}
	if detail[0].Msg != "Value error, refresh_cron is not a valid cron expression: '0 3 * * 8'" {
		t.Fatalf("msg = %q", detail[0].Msg)
	}
	if !strings.Contains(detail[0].Msg, "0 3 * * 8") {
		t.Fatalf("msg must name the expression: %q", detail[0].Msg)
	}
}
