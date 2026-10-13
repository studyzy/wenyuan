package provider

import "testing"

func TestRegistryNamesAreSorted(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zebra", "alpha", "middle"} {
		r.Register(stubRegistration(name, false))
	}
	want := []string{"alpha", "middle", "zebra"}
	if got := r.Names(); !sameStrings(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestRegistryLookup(t *testing.T) {
	r := NewRegistry()
	r.Register(stubRegistration("none", false))
	if _, ok := r.Lookup("none"); !ok {
		t.Fatal("registered name not found")
	}
	if _, ok := r.Lookup("absent"); ok {
		t.Fatal("absent name reported as found")
	}
	// Lookup tolerates surrounding whitespace so it agrees with selection.
	if _, ok := r.Lookup("  none  "); !ok {
		t.Fatal("whitespace-padded name not found")
	}
}

// A blank name is ignored rather than registered as the empty provider.
func TestRegistryIgnoresBlankName(t *testing.T) {
	r := NewRegistry()
	r.Register(stubRegistration("  ", false))
	if got := r.Names(); len(got) != 0 {
		t.Fatalf("names = %v, want empty", got)
	}
}

// Re-registering a name replaces the earlier registration rather than
// accumulating duplicates, so the accepted-name list stays canonical.
func TestRegistryReplacesDuplicateName(t *testing.T) {
	r := NewRegistry()
	r.Register(Registration{Name: "dup", RequiresCredential: false})
	r.Register(Registration{Name: "dup", RequiresCredential: true})
	if got := r.Names(); !sameStrings(got, []string{"dup"}) {
		t.Fatalf("names = %v, want one entry", got)
	}
	reg, _ := r.Lookup("dup")
	if !reg.RequiresCredential {
		t.Fatal("the later registration did not win")
	}
}

func TestRegistryCopiesAcceptedNames(t *testing.T) {
	r := NewRegistry()
	r.Register(stubRegistration("a", false))
	r.Register(stubRegistration("b", false))
	want := []string{"a", "b"}
	if got := AcceptedNames(r); !sameStrings(got, want) {
		t.Fatalf("accepted = %v, want %v", got, want)
	}
	if got := AcceptedNames(nil); got != nil {
		t.Fatalf("nil registry accepted names = %v, want nil", got)
	}
}
