package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The reference validates a request body against its declared model, so a JSON
// `null` is a value for a field the model admits and a failure for every other.
// Each expectation below is the exact body the live reference serves for the
// same request, captured from
// POST /v1/default/banks/{bank_id}/memories/recall and .../memories, so a change
// to the type, location, message, input or ctx of any item fails here.
//
// The vocabulary the items are drawn from, one entry per declared type:
//
//	integer  int_type      "Input should be a valid integer"
//	number   float_type    "Input should be a valid number"
//	boolean  bool_type     "Input should be a valid boolean"
//	string   string_type   "Input should be a valid string"
//	list     list_type     "Input should be a valid list"
//	dict     dict_type     "Input should be a valid dictionary"
//	model    model_attributes_type "Input should be a valid dictionary or object to extract fields from"
//	Enum     enum          "Input should be 'a', 'b' or 'c'"        (ctx.expected)
//	Literal  literal_error "Input should be 'a' or 'b'"             (ctx.expected)
//	datetime datetime_type "Input should be a valid datetime"
//
// and, for the same types given a value of a kind they take only by coercion:
//
//	integer  int_parsing     (a string the integer parser refuses)
//	integer  int_from_float  (a number with a fractional part)
//	boolean  bool_parsing    (a word it does not know, or 2)
var nullVocabularyCases = []struct {
	name string
	path string
	body string
	want string
}{
	{
		"a null for an integer",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","max_tokens":null}`,
		`{"detail":[{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null}]}`,
	},
	{
		"a null for a boolean",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","trace":null}`,
		`{"detail":[{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null}]}`,
	},
	{
		"a null for a string",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":null}`,
		`{"detail":[{"type":"string_type","loc":["body","query"],"msg":"Input should be a valid string","input":null}]}`,
	},
	{
		"a null for a nested model",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","include":null}`,
		`{"detail":[{"type":"model_attributes_type","loc":["body","include"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null}]}`,
	},
	{
		"a null for a nested-model member",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","include":{"source_facts":{"max_tokens":null}}}`,
		`{"detail":[{"type":"int_type","loc":["body","include","source_facts","max_tokens"],"msg":"Input should be a valid integer","input":null}]}`,
	},
	{
		"a null for another member of a nested model",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","include":{"entities":{"max_tokens":null}}}`,
		`{"detail":[{"type":"int_type","loc":["body","include","entities","max_tokens"],"msg":"Input should be a valid integer","input":null}]}`,
	},
	{
		"a null for a Literal",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","tags_match":null}`,
		`{"detail":[{"type":"literal_error","loc":["body","tags_match"],"msg":"Input should be 'any', 'all', 'any_strict', 'all_strict' or 'exact'","input":null,` +
			`"ctx":{"expected":"'any', 'all', 'any_strict', 'all_strict' or 'exact'"}}]}`,
	},
	{
		"a null for an Enum",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","budget":null}`,
		`{"detail":[{"type":"enum","loc":["body","budget"],"msg":"Input should be 'low', 'mid' or 'high'","input":null,` +
			`"ctx":{"expected":"'low', 'mid' or 'high'"}}]}`,
	},
	{
		"a null for a datetime, with the sibling the window requires",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","temporal_window":{"start":null}}`,
		`{"detail":[{"type":"datetime_type","loc":["body","temporal_window","start"],"msg":"Input should be a valid datetime","input":null},` +
			`{"type":"missing","loc":["body","temporal_window","end"],"msg":"Field required","input":{"start":null}}]}`,
	},
	{
		"every null rejection of one body, in declaration order",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":null,"max_tokens":null,"trace":null}`,
		`{"detail":[{"type":"string_type","loc":["body","query"],"msg":"Input should be a valid string","input":null},` +
			`{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null},` +
			`{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null}]}`,
	},
	{
		"a null rejection and a wrong-typed value are reported together, in declaration order",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","max_tokens":"abc","trace":null}`,
		`{"detail":[{"type":"int_parsing","loc":["body","max_tokens"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"abc"},` +
			`{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null}]}`,
	},
	{
		"a fractional number and a null are reported together",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","max_tokens":1.5,"trace":null}`,
		`{"detail":[{"type":"int_from_float","loc":["body","max_tokens"],"msg":"Input should be a valid integer, got a number with a fractional part","input":1.5},` +
			`{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null}]}`,
	},
	{
		"a null member of a model field is the shape failure for that field",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","include":{"entities":true},"max_tokens":null}`,
		`{"detail":[{"type":"int_type","loc":["body","max_tokens"],"msg":"Input should be a valid integer","input":null},` +
			`{"type":"model_attributes_type","loc":["body","include","entities"],"msg":"Input should be a valid dictionary or object to extract fields from","input":true}]}`,
	},
	{
		"a required member missing beside a null is reported in field order",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"include":null}`,
		`{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required","input":{"include":null}},` +
			`{"type":"model_attributes_type","loc":["body","include"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null}]}`,
	},
	{
		"a null body is the missing required field",
		"/v1/default/banks/{bank_id}/memories/recall",
		`null`,
		`{"detail":[{"type":"missing","loc":["body"],"msg":"Field required","input":null}]}`,
	},
	{
		"a null element of a union list is reported once per alternative",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","tag_groups":[null]}`,
		`{"detail":[` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupLeaf"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupAnd"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupOr"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupNot"],"msg":"Input should be a valid dictionary or object to extract fields from","input":null}]}`,
	},
	{
		"an object that fits no union alternative reports each alternative's own failure",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","tag_groups":[{"tag":null}]}`,
		`{"detail":[` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupLeaf","tags"],"msg":"Field required","input":{"tag":null}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Field required","input":{"tag":null}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"tag":null}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"tag":null}}]}`,
	},
	{
		"a null element of an untagged string list is a string failure",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","types":["world",null]}`,
		`{"detail":[{"type":"string_type","loc":["body","types",1],"msg":"Input should be a valid string","input":null}]}`,
	},
	{
		"a value outside an Enum is named the same way as a null",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","budget":5,"trace":null}`,
		`{"detail":[{"type":"enum","loc":["body","budget"],"msg":"Input should be 'low', 'mid' or 'high'","input":5,` +
			`"ctx":{"expected":"'low', 'mid' or 'high'"}},` +
			`{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null}]}`,
	},
	{
		"a value of the wrong kind for a list field",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","tags":"abc","trace":null}`,
		`{"detail":[{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null},` +
			`{"type":"list_type","loc":["body","tags"],"msg":"Input should be a valid list","input":"abc"}]}`,
	},
	{
		"a boolean word the framework does not know",
		"/v1/default/banks/{bank_id}/memories/recall",
		`{"query":"x","prefer_observations":" Yes","trace":null}`,
		`{"detail":[{"type":"bool_parsing","loc":["body","prefer_observations"],"msg":"Input should be a valid boolean, unable to interpret input","input":" Yes"},` +
			`{"type":"bool_type","loc":["body","trace"],"msg":"Input should be a valid boolean","input":null}]}`,
	},
	{
		"a null for an item's member keeps the full item path",
		"/v1/default/banks/{bank_id}/memories",
		`{"items":[{"content":"x","metadata":{"z":null,"a":1}}]}`,
		`{"detail":[{"type":"string_type","loc":["body","items",0,"metadata","z"],"msg":"Input should be a valid string","input":null},` +
			`{"type":"string_type","loc":["body","items",0,"metadata","a"],"msg":"Input should be a valid string","input":1}]}`,
	},
	{
		"a null for a union field is reported once per alternative",
		"/v1/default/banks/{bank_id}/memories",
		`{"items":[{"content":null}]}`,
		`{"detail":[{"type":"string_type","loc":["body","items",0,"content","str"],"msg":"Input should be a valid string","input":null},` +
			`{"type":"list_type","loc":["body","items",0,"content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]"],"msg":"Input should be a valid list","input":null}]}`,
	},
	{
		"a block element whose tag names no alternative",
		"/v1/default/banks/{bank_id}/memories",
		`{"items":[{"content":[{"type":null}]}]}`,
		`{"detail":[{"type":"string_type","loc":["body","items",0,"content","str"],"msg":"Input should be a valid string","input":[{"type":null}]},` +
			`{"type":"union_tag_invalid","loc":["body","items",0,"content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0],"msg":"Input tag 'None' found using 'type' does not match any of the expected tags: 'text', 'image', 'file'","input":{"type":null},"ctx":{"discriminator":"'type'","tag":"None","expected_tags":"'text', 'image', 'file'"}}]}`,
	},
	{
		"a block element whose alternative is chosen by its tag keeps the tag in the path",
		"/v1/default/banks/{bank_id}/memories",
		`{"items":[{"content":[{"type":"text","text":null}]}]}`,
		`{"detail":[{"type":"string_type","loc":["body","items",0,"content","str"],"msg":"Input should be a valid string","input":[{"type":"text","text":null}]},` +
			`{"type":"string_type","loc":["body","items",0,"content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0,"text","text"],"msg":"Input should be a valid string","input":null}]}`,
	},
}

func TestNullVocabulary(t *testing.T) {
	for _, testCase := range nullVocabularyCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := schemaForPattern("POST " + testCase.path)
			if root == nil {
				t.Fatalf("the route declares no request schema: %s", testCase.path)
			}
			got, err := json.Marshal(ValidationFailure{Errors: nullRejectionItems([]byte(testCase.body), root)})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if string(got) != testCase.want {
				t.Fatalf("body = %s\nwant   = %s", got, testCase.want)
			}
		})
	}
}

// A `null` for a declaration that admits one is a value, not a failure: the
// walk reports nothing for it and the request keeps its ordinary handling.
func TestNullForNullableFieldIsAccepted(t *testing.T) {
	for _, body := range []string{
		`{"query":"x","types":null}`,
		`{"query":"x","tags":null}`,
		`{"query":"x","query_timestamp":null}`,
		`{"query":"x","temporal_window":null}`,
		`{"query":"x","tag_groups":null}`,
		`{"query":"x","min_scores":{"semantic":null}}`,
		`{"query":"x","include":{"entities":null}}`,
		`{"query":"x","include":{"chunks":null}}`,
		`{"query":"x","include":{"source_facts":null}}`,
	} {
		if items := nullRejectionItems([]byte(body), schemaForPattern("POST /v1/default/banks/{bank_id}/memories/recall")); len(items) != 0 {
			t.Errorf("%s -> %+v", body, items)
		}
	}
	for _, body := range []string{
		`{"items":[{"content":"x","timestamp":null}]}`,
		`{"items":[{"content":"x","metadata":null}]}`,
		`{"items":[{"content":"x","entities":null}]}`,
	} {
		if items := nullRejectionItems([]byte(body), schemaForPattern("POST /v1/default/banks/{bank_id}/memories")); len(items) != 0 {
			t.Errorf("%s -> %+v", body, items)
		}
	}
}

// TestDecodeLaxReportsNullRejections checks the decode path itself: a body that
// refuses a `null` comes back as the walk's structured failure, and one that
// does not is decoded exactly as before.
func TestDecodeLaxReportsNullRejections(t *testing.T) {
	root := schemaForPattern("POST /v1/default/banks/{bank_id}/memories/recall")
	target := &struct {
		Query     string         `json:"query"`
		MaxTokens *int           `json:"max_tokens"`
		Include   map[string]any `json:"include"`
	}{}
	_, err := decodeLaxSchema([]byte(`{"query":"x","max_tokens":null}`), target, root)
	failure, ok := err.(*ValidationFailure)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	if len(failure.Errors) != 1 || failure.Errors[0].Type != "int_type" {
		t.Fatalf("errors = %+v", failure.Errors)
	}
	body, err := decodeLaxSchema([]byte(`{"query":"x","max_tokens":5}`), target, root)
	if err != nil {
		t.Fatalf("a body with no null was refused: %v", err)
	}
	if string(body) != `{"query":"x","max_tokens":5}` || *target.MaxTokens != 5 {
		t.Fatalf("body = %s, max_tokens = %v", body, target.MaxTokens)
	}
}

// A body that carries no `null` token is never walked: every rejection it
// carries is one the existing decode machinery already reports, and the walk
// would describe it a second time.
func TestNullWalkSkipsBodiesWithoutNull(t *testing.T) {
	root := schemaForPattern("POST /v1/default/banks/{bank_id}/memories/recall")
	if root == nil {
		t.Fatal("the recall route declares no request schema")
	}
	body := `{"query":"x","max_tokens":"abc","trace":"nope"}`
	if items := nullRejectionItems([]byte(body), root); len(items) != 0 {
		t.Fatalf("items = %+v", items)
	}
}

// Every mapped model resolves to a schema the served contract declares, so a
// renamed schema cannot leave a route's body silently unwalked.
func TestRequestSchemaOverridesResolve(t *testing.T) {
	for model, schema := range requestSchemaOverrides {
		if schemaNodeByName(schema) == nil {
			t.Errorf("%s maps to %q, which the served contract does not declare", model, schema)
		}
	}
}

// A route the served contract declares resolves to its body schema, and an
// unknown pattern resolves to nothing rather than to some other route's schema.
func TestSchemaForPattern(t *testing.T) {
	if node := schemaForPattern("POST /v1/default/banks/{bank_id}/memories/recall"); node == nil {
		t.Fatal("the recall pattern does not resolve")
	}
	if node := schemaForPattern("POST /v1/default/banks/{bank_id}/nonesuch"); node != nil {
		t.Fatalf("an unknown pattern resolved to %+v", node)
	}
}

// The walk reads the declaration the route matched, not the model's own name.
func TestSchemaForRequestPrefersRoute(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", nil)
	r.Pattern = "POST /v1/default/banks/{bank_id}/memories/recall"
	node := schemaForRequest(r, struct{}{})
	if node == nil || node.members[0].name != "query" {
		t.Fatalf("node = %+v", node)
	}
}
