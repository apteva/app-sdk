package sdk

import (
	"context"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"folder/invoices/**", "folder/invoices/q3/x.pdf", true},
		{"folder/invoices/**", "folder/salaries/x.pdf", false},
		{"folder/invoices/q3/*", "folder/invoices/q3/x.pdf", true},
		{"folder/invoices/q3/*", "folder/invoices/q3/sub/x.pdf", false}, // single * doesn't cross /
		{"folder/invoices/q3/**", "folder/invoices/q3/sub/x.pdf", true}, // ** does
		{"folder/invoices/", "folder/invoices/", true},
		{"folder/invoices", "folder/invoices/q3", false}, // exact, no trailing
		{"*", "anything", true},
		{"", "", true},
		{"", "a", false},
	}
	for _, tc := range cases {
		got := globMatch(tc.pattern, tc.s)
		if got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

// Default-allow back-compat: nil caller treats everything as allowed.
func TestCaller_Nil_AllowsEverything(t *testing.T) {
	var c *Caller
	if !c.Allows("files.delete", "folder/anything") {
		t.Fatal("nil caller should allow")
	}
	if !c.Has("anything") {
		t.Fatal("nil caller Has() should be true")
	}
}

// Empty caller (no grants) with default "allow" still allows; with
// default "deny" forbids.
func TestCaller_DefaultEffect(t *testing.T) {
	allow := &Caller{DefaultEffect: "allow"}
	if !allow.Allows("files.read", "folder/x") {
		t.Fatal("default-allow should permit")
	}
	deny := &Caller{DefaultEffect: "deny"}
	if deny.Allows("files.read", "folder/x") {
		t.Fatal("default-deny should refuse")
	}
}

func TestCaller_GlobScope(t *testing.T) {
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob", Picker: "tree"}},
	}
	cases := []struct {
		path string
		want bool
	}{
		{"folder/invoices/q3/r.pdf", true},
		{"folder/invoices/q4", true},
		{"folder/salaries/x", false},
		{"folder/invoices", false}, // pattern requires `/...` after invoices
	}
	for _, tc := range cases {
		got := c.Allows("files.read", tc.path)
		if got != tc.want {
			t.Errorf("Allows(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	// Wrong permission, even on an in-scope resource → denied.
	if c.Allows("files.delete", "folder/invoices/x.pdf") {
		t.Fatal("delete should not be allowed when only read was granted")
	}
}

func TestCaller_DenyOverridesAllow(t *testing.T) {
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**"},
			{Effect: "deny", Permission: "files.read", Resource: "folder/invoices/secret/**"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob"}},
	}
	if !c.Allows("files.read", "folder/invoices/q3/x") {
		t.Fatal("non-secret allowed")
	}
	if c.Allows("files.read", "folder/invoices/secret/x") {
		t.Fatal("explicit deny must win over the broader allow")
	}
}

func TestCaller_IDSetMatcher(t *testing.T) {
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "posts.publish", Resource: "tw_123,tw_456"},
		},
		Resources: []ResourceDecl{{Name: "account", Matcher: "id_set"}},
	}
	if !c.Allows("posts.publish", "account/tw_123") {
		t.Fatal("granted account should be allowed")
	}
	if c.Allows("posts.publish", "account/tw_999") {
		t.Fatal("non-granted account should be refused")
	}
}

func TestCaller_StarPatternIsUniversal(t *testing.T) {
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "*"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob"}},
	}
	if !c.Allows("files.read", "folder/anywhere") {
		t.Fatal("`*` should match everything regardless of matcher")
	}
}

func TestFilter(t *testing.T) {
	type item struct{ folder string }
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob"}},
	}
	items := []item{
		{"invoices/q3/a"},
		{"salaries/jan"},
		{"invoices/q4/b"},
		{"hr/x"},
	}
	got := Filter(c, "files.read", items, func(i item) string {
		return "folder/" + i.folder
	})
	if len(got) != 2 || got[0].folder != "invoices/q3/a" || got[1].folder != "invoices/q4/b" {
		t.Fatalf("Filter = %v, want only invoices/*", got)
	}
}

// FilterTree — the user's exact scenario. Agent scoped to invoices/**
// lists folders at root; should see only `invoices/`, not other
// top-levels.
func TestFilterTree_Navigable_AncestorStubsVisible(t *testing.T) {
	type folder struct{ path string }
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob", Picker: "tree", ListingVisibility: "navigable"}},
	}
	// Root listing — the stub `invoices` should appear so the agent
	// can navigate to its subtree, others should not.
	root := []folder{{"invoices"}, {"salaries"}, {"hr"}, {"marketing"}}
	got := FilterTree(c, "files.read", root,
		func(f folder) string { return "folder/" + f.path },
		func(f folder) string { return f.path })
	if len(got) != 1 || got[0].path != "invoices" {
		t.Fatalf("root listing = %v, want only [invoices]", got)
	}
	// Inside invoices/ — the agent should see all its children
	// because they're entirely inside scope.
	inside := []folder{{"invoices/q3"}, {"invoices/q4"}, {"invoices/q1"}}
	got = FilterTree(c, "files.read", inside,
		func(f folder) string { return "folder/" + f.path },
		func(f folder) string { return f.path })
	if len(got) != 3 {
		t.Fatalf("inside-scope listing dropped items: %v", got)
	}
}

// Narrower scope — `folder/invoices/q3/**`. At root, `invoices` is
// still visible (ancestor of the allowed subtree). At `invoices/`,
// only `q3` is visible.
func TestFilterTree_NarrowScope_OnlyAncestorsToTheLeaf(t *testing.T) {
	type folder struct{ path string }
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "folder/invoices/q3/**"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob", Picker: "tree", ListingVisibility: "navigable"}},
	}
	root := []folder{{"invoices"}, {"salaries"}}
	got := FilterTree(c, "files.read", root,
		func(f folder) string { return "folder/" + f.path },
		func(f folder) string { return f.path })
	if len(got) != 1 || got[0].path != "invoices" {
		t.Fatalf("root = %v, want only [invoices]", got)
	}
	siblings := []folder{{"invoices/q3"}, {"invoices/q4"}, {"invoices/q1"}}
	got = FilterTree(c, "files.read", siblings,
		func(f folder) string { return "folder/" + f.path },
		func(f folder) string { return f.path })
	if len(got) != 1 || got[0].path != "invoices/q3" {
		t.Fatalf("invoices/ listing = %v, want only [invoices/q3]", got)
	}
}

func TestFilterTree_ScopedOnly_NoStubs(t *testing.T) {
	type folder struct{ path string }
	c := &Caller{
		DefaultEffect: "deny",
		Grants: []Grant{
			{Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**"},
		},
		Resources: []ResourceDecl{{Name: "folder", Matcher: "glob", Picker: "tree", ListingVisibility: "scoped_only"}},
	}
	root := []folder{{"invoices"}, {"salaries"}, {"hr"}}
	got := FilterTree(c, "files.read", root,
		func(f folder) string { return "folder/" + f.path },
		func(f folder) string { return f.path })
	// scoped_only refuses ancestor stubs — root invoices isn't itself
	// in scope (only its children are).
	if len(got) != 0 {
		t.Fatalf("scoped_only root listing should be empty, got %v", got)
	}
}

func TestSubstituteResource(t *testing.T) {
	args := map[string]any{"folder": "invoices/q3", "id": 42}
	got, err := substituteResource("folder/{arg.folder}", args)
	if err != nil || got != "folder/invoices/q3" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = substituteResource("file/{arg.id}", args)
	if err != nil || got != "file/42" {
		t.Fatalf("got %q err %v", got, err)
	}
	// Missing required arg fails closed.
	_, err = substituteResource("folder/{arg.missing}", args)
	if err == nil {
		t.Fatal("missing arg must fail")
	}
	// Optional missing arg substitutes empty.
	got, err = substituteResource("folder/{arg.missing?}", args)
	if err != nil || got != "folder/" {
		t.Fatalf("optional missing: got %q err %v", got, err)
	}
}

func TestForbidden(t *testing.T) {
	err := Forbidden("files.delete", "folder/x")
	if !IsForbidden(err) {
		t.Fatal("IsForbidden should be true for *ErrForbidden")
	}
	if err.Error() != "forbidden: files.delete on folder/x" {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestCallerFromContext(t *testing.T) {
	if got := CallerFrom(nil); got != nil {
		t.Fatal("nil context should return nil caller")
	}
	if got := CallerFrom(context.Background()); got != nil {
		t.Fatal("bare context should return nil caller")
	}
	c := &Caller{InstanceID: 7}
	got := CallerFrom(WithCaller(context.Background(), c))
	if got == nil || got.InstanceID != 7 {
		t.Fatalf("got %+v", got)
	}
}
