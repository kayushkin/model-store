package modelstore

import (
	"path/filepath"
	"testing"
)

// newTestStore opens a throwaway store with two real models so role
// assignment has something valid to point at.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	for _, m := range []Model{
		{ID: "claude-fable-5-1", Provider: "anthropic", Name: "Fable 5.1", MaxTokens: 1000000, Enabled: true},
		{ID: "gpt-5.6-luna", Provider: "openai", Name: "Luna", MaxTokens: 400000, Enabled: true},
	} {
		if err := s.AddModel(m); err != nil {
			t.Fatalf("seed model %s: %v", m.ID, err)
		}
	}
	return s
}

func TestSetAndResolveRole(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetRole(RoleEfficient, "gpt-5.6-luna"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	m, err := s.ResolveRole(RoleEfficient)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if m.ID != "gpt-5.6-luna" {
		t.Fatalf("efficient resolved to %q, want gpt-5.6-luna", m.ID)
	}
}

func TestSetRoleRejectsUnknownRole(t *testing.T) {
	s := newTestStore(t)
	// A typo must not create a silent fourth role.
	if err := s.SetRole("effcient", "gpt-5.6-luna"); err == nil {
		t.Fatal("SetRole accepted an unknown role name; want rejection")
	}
}

func TestSetRoleRejectsUnknownModel(t *testing.T) {
	s := newTestStore(t)
	// A role may only ever name a model that exists.
	if err := s.SetRole(RoleBest, "claude-imaginary-9"); err == nil {
		t.Fatal("SetRole accepted a nonexistent model; want rejection")
	}
}

func TestResolveUnassignedRoleFailsLoud(t *testing.T) {
	s := newTestStore(t)
	// No fabricated fallback: an unassigned role is an error, not a guess.
	if _, err := s.ResolveRole(RoleDefault); err == nil {
		t.Fatal("ResolveRole on an unassigned role returned no error; want fail-loud")
	}
}

func TestSetRoleIsIdempotentUpsert(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetRole(RoleDefault, "claude-fable-5-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole(RoleDefault, "gpt-5.6-luna"); err != nil {
		t.Fatal(err)
	}
	m, err := s.ResolveRole(RoleDefault)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "gpt-5.6-luna" {
		t.Fatalf("re-set role resolved to %q, want gpt-5.6-luna", m.ID)
	}
	roles, err := s.Roles()
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role row after upsert, got %d", len(roles))
	}
}

func TestAllModelsWithStatusCarriesAliases(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddAlias("gpt-5.6-luna", "luna"); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.AllModelsWithStatus()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ms := range statuses {
		if ms.ID == "gpt-5.6-luna" {
			found = true
			if len(ms.Aliases) != 1 || ms.Aliases[0] != "luna" {
				t.Fatalf("aliases = %v, want [luna] — the aliases:null regression is back", ms.Aliases)
			}
		}
	}
	if !found {
		t.Fatal("gpt-5.6-luna missing from AllModelsWithStatus")
	}
}
