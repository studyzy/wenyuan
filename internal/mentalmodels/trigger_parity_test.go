package mentalmodels

import (
	"encoding/json"
	"net/http"
	"testing"
)

// triggerItem is one structured 422 item with its input kept raw, so a test can
// assert the value the framework echoes back (a string, a number, an object).
type triggerItem struct {
	Type  string `json:"type"`
	Loc   []any  `json:"loc"`
	Msg   string `json:"msg"`
	Input any    `json:"input"`
}

// triggerDetail reads the structured 422 envelope of a PATCH on the fixture
// model, keeping every item.
func triggerDetail(t *testing.T, mux *http.ServeMux, body string) []triggerItem {
	t.Helper()
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail []triggerItem `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Detail
}

// newTriggerServer builds the route table over one stored model, so a patch has
// something to merge onto.
func newTriggerServer(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := newServer(t)
	created := send(mux, http.MethodPost, "/v1/default/banks/b1/mental-models",
		`{"id":"mm-1","name":"model","source_query":"what changed?"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	return mux
}

// locString joins an item's location the way the framework renders it.
func locString(loc []any) string {
	out := ""
	for i, part := range loc {
		if i > 0 {
			out += "."
		}
		switch typed := part.(type) {
		case string:
			out += typed
		default:
			raw, _ := json.Marshal(typed)
			out += string(raw)
		}
	}
	return out
}

// A trigger value that is not an object is the framework's model-shape failure,
// named model_attributes_type with the offending VALUE as input. Reporting it as
// a dict_type item loses both the name and the value, so a client cannot tell
// which of its members was the wrong shape.
func TestTriggerNonObjectIsModelShapeFailure(t *testing.T) {
	mux := newTriggerServer(t)
	cases := []struct {
		body  string
		input string
	}{
		{`{"trigger":[]}`, `[]`},
		{`{"trigger":5}`, `5`},
		{`{"trigger":"text"}`, `"text"`},
		{`{"trigger":true}`, `true`},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			detail := triggerDetail(t, mux, tc.body)
			if len(detail) != 1 {
				t.Fatalf("detail = %+v, want one item", detail)
			}
			if detail[0].Type != "model_attributes_type" {
				t.Fatalf("type = %q, want model_attributes_type", detail[0].Type)
			}
			if locString(detail[0].Loc) != "body.trigger" {
				t.Fatalf("loc = %v, want [body trigger]", detail[0].Loc)
			}
			got, _ := json.Marshal(detail[0].Input)
			if string(got) != tc.input {
				t.Fatalf("input = %s, want %s", got, tc.input)
			}
		})
	}
}

// The framework runs a trigger's checks in the model's field order, not in the
// order the client sent the members, and a model-level validator runs after
// every field-level one. Each failure is reported, and a member whose own type
// check refused it never reaches its custom validator, so it contributes no
// second item.
func TestTriggerItemsFollowTheModelOrder(t *testing.T) {
	mux := newTriggerServer(t)
	detail := triggerDetail(t, mux,
		`{"trigger":{"tags_match":"no","fact_types":["a","b"],"min_refresh_interval_seconds":-1,`+
			`"budget":"nope","refresh_cron":"abc","mode":"bogus"}}`)

	wantLocs := []string{
		"body.trigger.budget",
		"body.trigger.mode",
		"body.trigger.refresh_cron",
		"body.trigger.min_refresh_interval_seconds",
		"body.trigger.fact_types.0",
		"body.trigger.fact_types.1",
		"body.trigger.tags_match",
	}
	if len(detail) != len(wantLocs) {
		t.Fatalf("detail = %+v\nwant %d items", detail, len(wantLocs))
	}
	for i, want := range wantLocs {
		if got := locString(detail[i].Loc); got != want {
			t.Fatalf("item %d loc = %q, want %q (full %+v)", i, got, want, detail)
		}
	}
	if detail[2].Type != "value_error" || detail[2].Msg !=
		"Value error, refresh_cron is not a valid cron expression: 'abc'" {
		t.Fatalf("refresh_cron item = %+v", detail[2])
	}
}

// The exclusivity rule is a model-level validator, and the framework skips one
// once any field-level check has failed the model. So the rule is reported on an
// otherwise valid trigger — as the whole trigger object, since that is the model's
// input — and NOT beside a member that already failed.
func TestTriggerExclusivityIsSkippedWhenAFieldFailed(t *testing.T) {
	mux := newTriggerServer(t)

	detail := triggerDetail(t, mux,
		`{"trigger":{"refresh_after_consolidation":true,"refresh_cron":"0 * * * *"}}`)
	if len(detail) != 1 || locString(detail[0].Loc) != "body.trigger" || detail[0].Type != "value_error" {
		t.Fatalf("detail = %+v, want the single model-level exclusivity item", detail)
	}
	input, ok := detail[0].Input.(map[string]any)
	if !ok || input["refresh_cron"] != "0 * * * *" {
		t.Fatalf("exclusivity input = %#v, want the whole trigger object", detail[0].Input)
	}
	if detail[0].Msg != "Value error, refresh_after_consolidation and refresh_cron are mutually "+
		"exclusive: a mental model refreshes either after consolidation or on a cron schedule, not both." {
		t.Fatalf("msg = %q", detail[0].Msg)
	}

	// A failing field member suppresses the model-level item entirely.
	for _, body := range []string{
		`{"trigger":{"tag_groups":[{"tags":["a"],"resolve":"fuzzy"}],` +
			`"refresh_after_consolidation":true,"refresh_cron":"0 * * * *"}}`,
		`{"trigger":{"fact_types":[],"refresh_after_consolidation":true,"refresh_cron":"0 * * * *"}}`,
		`{"trigger":{"budget":"bad","refresh_after_consolidation":true,"refresh_cron":"0 * * * *"}}`,
	} {
		detail := triggerDetail(t, mux, body)
		for _, item := range detail {
			if locString(item.Loc) == "body.trigger" {
				t.Fatalf("%s: exclusivity item must be suppressed when a field failed, got %+v", body, detail)
			}
		}
		if len(detail) == 0 {
			t.Fatalf("%s: want the field-level item", body)
		}
	}
}

// A boolean member takes the framework's lax coercion: the accepted words and
// 0/1 are booleans, and the model stores what they mean, not the text. A word it
// does not know (and a number outside 0/1) is the framework's bool_parsing item,
// because a string and a number both reach its lax conversion; every other kind
// is the plain bool_type mismatch, because it never does.
func TestTriggerBooleanMembersTakeLaxCoercion(t *testing.T) {
	mux := newTriggerServer(t)
	accepted := []struct {
		body   string
		member string
		want   any
	}{
		{`{"trigger":{"keep_trace":"yes"}}`, "keep_trace", true},
		{`{"trigger":{"keep_trace":"off"}}`, "keep_trace", false},
		{`{"trigger":{"keep_trace":1}}`, "keep_trace", true},
		{`{"trigger":{"exclude_mental_models":"true"}}`, "exclude_mental_models", true},
		{`{"trigger":{"refresh_after_consolidation":"on"}}`, "refresh_after_consolidation", true},
		{`{"trigger":{"refresh_after_consolidation":0}}`, "refresh_after_consolidation", false},
	}
	for _, tc := range accepted {
		t.Run(tc.body, func(t *testing.T) {
			rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			var model Model
			if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
				t.Fatal(err)
			}
			if got := model.Trigger[tc.member]; got != tc.want {
				t.Fatalf("%s = %#v, want %#v", tc.member, got, tc.want)
			}
		})
	}

	// Numbers outside 0/1 and unknown words reach the lax conversion and fail to
	// parse; a null, an object, a list and a fractional number never do, so they
	// are the framework's plain type mismatch instead. The split is observable and
	// was checked against the live reference on 2026-10-20.
	for body, wantType := range map[string]string{
		`{"trigger":{"keep_trace":2}}`:       "bool_parsing",
		`{"trigger":{"keep_trace":-1}}`:      "bool_parsing",
		`{"trigger":{"keep_trace":"maybe"}}`: "bool_parsing",
		`{"trigger":{"keep_trace":null}}`:    "bool_type",
		`{"trigger":{"keep_trace":{}}}`:      "bool_type",
		`{"trigger":{"keep_trace":[]}}`:      "bool_type",
		`{"trigger":{"keep_trace":1.5}}`:     "bool_type",
	} {
		detail := triggerDetail(t, mux, body)
		if len(detail) != 1 || detail[0].Type != wantType {
			t.Fatalf("%s: detail = %+v, want one %s item", body, detail, wantType)
		}
	}
}

// The integer members take a numeric string the way Python's int() reads one,
// and store the number in the trigger rather than the text.
func TestTriggerIntegerMembersTakeNumericStrings(t *testing.T) {
	mux := newTriggerServer(t)
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"min_refresh_interval_seconds":"5","recall_max_tokens":"7",`+
			`"reflect_search_observations_max_tokens":"9"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	for member, want := range map[string]float64{
		"min_refresh_interval_seconds":           5,
		"recall_max_tokens":                      7,
		"reflect_search_observations_max_tokens": 9,
	} {
		if got, _ := model.Trigger[member].(float64); got != want {
			t.Fatalf("%s = %#v, want %v", member, model.Trigger[member], want)
		}
	}
}

// A tag group is a union the framework reports one item per alternative for,
// each under the alternative's own name. A well-formed group that fits one
// alternative is accepted; one that fits none reports every alternative.
func TestTriggerTagGroupsReportPerAlternative(t *testing.T) {
	mux := newTriggerServer(t)

	accepted := []string{
		`{"trigger":{"tag_groups":[{"tags":["a"],"match":"all"}]}}`,
		`{"trigger":{"tag_groups":[{"not":{"tags":["a"]}}]}}`,
		`{"trigger":{"tag_groups":[{"and":[{"tags":["a"]},{"or":[{"tags":["b"]}]}]}]}}`,
		`{"trigger":{"tag_groups":[]}}`,
		`{"trigger":{"tag_groups":null}}`,
	}
	for _, body := range accepted {
		if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1", body); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", body, rec.Code, rec.Body.String())
		}
	}

	detail := triggerDetail(t, mux, `{"trigger":{"tag_groups":[{"tags":["a"],"match":"BOGUS"}]}}`)
	locs := make([]string, len(detail))
	for i, item := range detail {
		locs[i] = locString(item.Loc)
	}
	want := []string{
		"body.trigger.tag_groups.0.TagGroupLeaf.match",
		"body.trigger.tag_groups.0.TagGroupAnd.and",
		"body.trigger.tag_groups.0.TagGroupOr.or",
		"body.trigger.tag_groups.0.TagGroupNot.not",
	}
	if len(locs) != len(want) {
		t.Fatalf("locs = %v, want %v", locs, want)
	}
	for i := range want {
		if locs[i] != want[i] {
			t.Fatalf("locs = %v, want %v", locs, want)
		}
	}
	// An empty group names each alternative's own required member.
	detail = triggerDetail(t, mux, `{"trigger":{"tag_groups":[{}]}}`)
	wantMissing := []string{
		"body.trigger.tag_groups.0.TagGroupLeaf.tags",
		"body.trigger.tag_groups.0.TagGroupAnd.and",
		"body.trigger.tag_groups.0.TagGroupOr.or",
		"body.trigger.tag_groups.0.TagGroupNot.not",
	}
	if len(detail) != len(wantMissing) {
		t.Fatalf("detail = %+v, want %d items", detail, len(wantMissing))
	}
	for i, want := range wantMissing {
		if detail[i].Type != "missing" || locString(detail[i].Loc) != want {
			t.Fatalf("item %d = %+v, want missing at %s", i, detail[i], want)
		}
	}
}

// A response schema is checked against the structured-output extractor's
// contract at the API boundary: the schema, its properties and its required list
// each have a shape, and an unusable one is a `Value error` on the member.
func TestTriggerResponseSchemaIsValidated(t *testing.T) {
	mux := newTriggerServer(t)
	cases := map[string]string{
		`{"trigger":{"response_schema":{"properties":{}}}}`:                                       "Value error, response_schema must define a non-empty 'properties' object",
		`{"trigger":{"response_schema":{}}}`:                                                      "Value error, response_schema must define a non-empty 'properties' object",
		`{"trigger":{"response_schema":{"type":"string","properties":{"a":{"type":"string"}}}}}`:  `Value error, response_schema must be an object schema (its "type" must be "object")`,
		`{"trigger":{"response_schema":{"properties":{"a":5}}}}`:                                  "Value error, response_schema property 'a' must be an object",
		`{"trigger":{"response_schema":{"properties":{"a":{"type":"bogus"}}}}}`:                   "Value error, response_schema property 'a' has unsupported type 'bogus'; expected one of ['array', 'boolean', 'integer', 'number', 'object', 'string']",
		`{"trigger":{"response_schema":{"properties":{"a":{"type":"string"}},"required":"a"}}}`:   "Value error, response_schema 'required' must be a list of property names",
		`{"trigger":{"response_schema":{"properties":{"a":{"type":"string"}},"required":["b"]}}}`: "Value error, response_schema 'required' references unknown properties: ['b']",
	}
	for body, want := range cases {
		t.Run(body, func(t *testing.T) {
			detail := triggerDetail(t, mux, body)
			if len(detail) != 1 || detail[0].Msg != want {
				t.Fatalf("detail = %+v\nwant msg %q", detail, want)
			}
			if locString(detail[0].Loc) != "body.trigger.response_schema" {
				t.Fatalf("loc = %v", detail[0].Loc)
			}
		})
	}
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"response_schema":{"properties":{"a":{"type":"string"}}},"keep_trace":true}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a usable schema: %d %s", rec.Code, rec.Body.String())
	}
}

// An exclusion list is a list of identifiers: the list itself and each entry are
// checked, since the reference projects them through a list[str] member.
func TestTriggerExcludeMentalModelIdsShape(t *testing.T) {
	mux := newTriggerServer(t)
	detail := triggerDetail(t, mux, `{"trigger":{"exclude_mental_model_ids":[1,"ok"]}}`)
	if len(detail) != 1 || detail[0].Type != "string_type" {
		t.Fatalf("detail = %+v, want one string_type item", detail)
	}
	if locString(detail[0].Loc) != "body.trigger.exclude_mental_model_ids.0" {
		t.Fatalf("loc = %v", detail[0].Loc)
	}
	detail = triggerDetail(t, mux, `{"trigger":{"exclude_mental_model_ids":"a"}}`)
	if len(detail) != 1 || detail[0].Type != "list_type" {
		t.Fatalf("detail = %+v, want one list_type item", detail)
	}
}

// A boolean sent for an integer member is the integer it coerces to, because the
// framework's integer type takes a bool through its lax path — so `true` stores
// 1 and `false` stores 0 rather than being refused or read as "not supplied".
// The distinction matters downstream: a stored 0 is a real bound (refresh every
// tick), while a missing member means the default.
func TestTriggerIntegerMembersReadBooleansAsNumbers(t *testing.T) {
	mux := newTriggerServer(t)
	for raw, want := range map[string]float64{"true": 1, "false": 0} {
		rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
			`{"trigger":{"min_refresh_interval_seconds":`+raw+`}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (%s)", raw, rec.Code, rec.Body.String())
		}
		var model Model
		if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
			t.Fatal(err)
		}
		got, _ := model.Trigger["min_refresh_interval_seconds"].(float64)
		if got != want {
			t.Fatalf("min_refresh_interval_seconds from %s = %#v, want %v",
				raw, model.Trigger["min_refresh_interval_seconds"], want)
		}
	}
}

// An integer member sent as a number keeps that number. Reading the body into
// map[string]any and then re-encoding the object for the patch used to turn a
// whole number into a float and lose it, storing null where the client had set a
// value; the reference stores what it was sent.
func TestTriggerIntegerMembersKeepLargeValues(t *testing.T) {
	mux := newTriggerServer(t)
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"min_refresh_interval_seconds":2048}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if got, _ := model.Trigger["min_refresh_interval_seconds"].(float64); got != 2048 {
		t.Fatalf("min_refresh_interval_seconds = %#v, want 2048", model.Trigger["min_refresh_interval_seconds"])
	}
}

// The two schedule members are exclusive whichever order they are stated in: a
// supplied refresh_cron clears a stale refresh_after_consolidation, and enabling
// consolidation clears a configured cron. A model is either driven by a schedule
// or refreshed with each consolidation, never both — verified against the live
// reference on 2026-10-20, where setting the flag on a cron-scheduled model
// answered with `refresh_cron: null`.
func TestTriggerScheduleMembersAreExclusive(t *testing.T) {
	mux := newTriggerServer(t)
	for _, step := range []string{
		`{"trigger":{"refresh_cron":"9 9 * * *"}}`,
		`{"trigger":{"refresh_after_consolidation":true}}`,
	} {
		if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1", step); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", step, rec.Code, rec.Body.String())
		}
	}
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/mental-models/mm-1", "")
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Trigger["refresh_cron"] != nil {
		t.Fatalf("refresh_cron = %#v, want null: enabling consolidation replaces a schedule",
			model.Trigger["refresh_cron"])
	}
	if model.Trigger["refresh_after_consolidation"] != true {
		t.Fatalf("refresh_after_consolidation = %#v, want true", model.Trigger["refresh_after_consolidation"])
	}
}

// A patch body whose trigger member is JSON null supplies no trigger: the model
// cannot read members out of a null, and an optional member stated as null is
// read as absent, so the patch names no column and the reference answers with its
// not-found (the same reply `{}` gets). Only a non-null, non-object value is the
// framework's model_attributes_type failure.
func TestUpdateTreatsNullTriggerAsNoColumn(t *testing.T) {
	mux := newTriggerServer(t)
	// The trigger was stored as null at creation, so the merge would state
	// nothing either way — the reply must still be 200, not the 422 a
	// model_attributes_type failure produces.
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"name":"renamed","trigger":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the null trigger ignored (%s)", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Name != "renamed" {
		t.Fatalf("name = %q, want renamed", model.Name)
	}
}

// A member stated as JSON null is stored as a null, because the reference merges
// `{**base, **supplied}` — the null is the value the client supplied, and it is
// how a member is cleared. Dropping null members from the merge instead would
// leave the old value in place, so a patch could never clear one.
func TestTriggerMergeStoresExplicitNulls(t *testing.T) {
	mux := newTriggerServer(t)
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"refresh_cron":"9 9 * * *","fact_types":["world"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/mental-models/mm-1",
		`{"trigger":{"refresh_cron":null,"fact_types":null}}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d (%s)", rec.Code, rec.Body.String())
	}
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/mental-models/mm-1", "")
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"refresh_cron", "fact_types"} {
		value, present := model.Trigger[member]
		if !present {
			t.Fatalf("%s is absent, want an explicit null: the merge states it", member)
		}
		if value != nil {
			t.Fatalf("%s = %#v, want null", member, value)
		}
	}
}
