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
		{"", false},
		{"-web", false},
		{".web", false},
		{"web site", false},
		// A colon would split the bank id it produces in the wrong place.
		{"web:site", false},
		{"web::site", false},
		{strings.Repeat("a", 65), false},
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
