package tenant

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateProjectID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"web", true},
		{"web2", true},
		{"w2b", true},
		{"a1", true},
		{"ab", true},
		{"x", false},
		{"", false},
		{"Web", false},
		{"_web", false},
		{"1web", false},
		{"web site", false},
		{"web::site", false},
		{strings.Repeat("a", 64), false},
	}
	for _, testCase := range cases {
		err := ValidateProjectID(testCase.id)
		if got := err == nil; got != testCase.want {
			t.Fatalf("ValidateProjectID(%q) err = %v, want valid = %v", testCase.id, err, testCase.want)
		}
		if err != nil && !errors.Is(err, ErrInvalidProject) {
			t.Fatalf("ValidateProjectID(%q) = %v, want ErrInvalidProject", testCase.id, err)
		}
	}
}

func TestValidateProjectName(t *testing.T) {
	// A name is presentation only — bank ids are built from the project id — so
	// anything a person might type is accepted, in any script.
	cases := []struct {
		name string
		want bool
	}{
		{"Web", true},
		{"web", true},
		{"Web-2", true},
		{"my.project", true},
		{"my_project", true},
		{"0", true},
		{"web site", true},
		// A colon is ordinary text now: it no longer participates in any id.
		{"web:site", true},
		{"web::site", true},
		{"产品:Web 后端", true},
		{"プロジェクト", true},
		{"🚀 rocket", true},
		{"", false},
		{"   ", false},
		{strings.Repeat("a", projectNameMaxRunes), true},
		{strings.Repeat("a", projectNameMaxRunes+1), false},
		// Counted in characters, not bytes: a Chinese name of the same length is
		// fine even though its byte count is three times larger.
		{strings.Repeat("产", projectNameMaxRunes), true},
		{strings.Repeat("产", projectNameMaxRunes+1), false},
	}
	for _, testCase := range cases {
		err := ValidateProjectName(testCase.name)
		if got := err == nil; got != testCase.want {
			t.Fatalf("ValidateProjectName(%q) err = %v, want valid = %v", testCase.name, err, testCase.want)
		}
	}
}

func TestProjectBankID(t *testing.T) {
	if got := ProjectBankID("acme", "Web"); got != "acme::Web" {
		t.Fatalf("ProjectBankID = %q, want acme::Web", got)
	}
	// Two teams may own a project of the same name; their banks differ by the
	// prefix, which is the whole point of the two-part id.
	if ProjectBankID("acme", "Web") == ProjectBankID("beta", "Web") {
		t.Fatal("two teams' same-named project banks collided")
	}
}

func TestMemberRoleValid(t *testing.T) {
	for _, role := range []MemberRole{RoleOwner, RoleMember} {
		if !role.Valid() {
			t.Fatalf("%q is not valid", role)
		}
	}
	if MemberRole("admin").Valid() {
		t.Fatal("admin role should not be valid")
	}
}

func TestRecordKindValue(t *testing.T) {
	if (Record{}).KindValue() != KindPersonal {
		t.Fatal("an empty kind should read as personal")
	}
	if (Record{Kind: KindTeam}).KindValue() != KindTeam {
		t.Fatal("team kind was not preserved")
	}
	if !(Record{Kind: KindTeam}).IsTeam() || (Record{}).IsTeam() {
		t.Fatal("IsTeam disagrees with KindValue")
	}
}

// TestProjectBankIDUsesTheProjectID pins the rule the whole addressing scheme
// rests on: a bank id's suffix is the project *id*, never its display name.
//
// The name is a label a person reads, so it may be Chinese, may carry a colon, and
// may change meaning freely; none of that may move where the data lives.
func TestProjectBankIDUsesTheProjectID(t *testing.T) {
	cases := []struct {
		tenantID  string
		projectID string
		want      string
	}{
		{"acme", "web", "acme::web"},
		{"acme", "web_api", "acme::web_api"},
		// A project whose name differs from its id still addresses by the id.
		{"acme", "labelled", "acme::labelled"},
	}
	for _, tc := range cases {
		if got := ProjectBankID(tc.tenantID, tc.projectID); got != tc.want {
			t.Fatalf("ProjectBankID(%q, %q) = %q, want %q", tc.tenantID, tc.projectID, got, tc.want)
		}
	}
	// The signature takes an id, so a name never reaches it: this is what keeps a
	// colon in a name from splitting the bank id in the wrong place.
	if got := ProjectBankID("acme", "web"); strings.HasSuffix(got, "Web") {
		t.Fatalf("bank id %q carries a display name", got)
	}
}
