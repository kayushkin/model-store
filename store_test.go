package modelstore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigration(t *testing.T) {
	// Create a DB with old schema
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}

	// Old schema
	_, err = db.Exec(`
		CREATE TABLE providers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			base_url TEXT,
			auth_type TEXT NOT NULL DEFAULT 'api_key'
		);
		CREATE TABLE models (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			name TEXT NOT NULL,
			max_tokens INTEGER DEFAULT 200000,
			input_cost REAL DEFAULT 0,
			output_cost REAL DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			priority INTEGER DEFAULT 100
		);
		CREATE TABLE model_aliases (
			alias TEXT PRIMARY KEY,
			model_id TEXT NOT NULL
		);
		CREATE TABLE credentials (
			provider TEXT PRIMARY KEY,
			auth_type TEXT NOT NULL,
			api_key TEXT,
			token TEXT,
			refresh_token TEXT,
			expires_at INTEGER,
			base_url TEXT
		);
		CREATE TABLE usage (
			date TEXT NOT NULL,
			agent TEXT NOT NULL,
			model TEXT NOT NULL,
			provider TEXT NOT NULL,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			requests INTEGER DEFAULT 0,
			cost_usd REAL DEFAULT 0,
			PRIMARY KEY (date, agent, model)
		);

		INSERT INTO providers VALUES ('anthropic', 'Anthropic', 'https://api.anthropic.com', 'oauth');
		INSERT INTO credentials VALUES ('anthropic', 'api_key', 'sk-test-key', '', '', 0, 'https://api.anthropic.com');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Open with new code — should migrate
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify migrated credential
	creds, err := s.ListCredentials("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 migrated credential, got %d", len(creds))
	}
	c := creds[0]
	if c.ID != "anthropic:default" {
		t.Errorf("expected id 'anthropic:default', got %q", c.ID)
	}
	if c.APIKey != "sk-test-key" {
		t.Errorf("expected api_key 'sk-test-key', got %q", c.APIKey)
	}
	if c.AuthType != "api_key" {
		t.Errorf("expected auth_type 'api_key', got %q", c.AuthType)
	}

	// Verify old table was renamed
	var cnt int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='credentials_old'`).Scan(&cnt)
	if cnt != 1 {
		t.Error("expected credentials_old table to exist")
	}
}

func TestResolveMultipleCredentials(t *testing.T) {
	s := tempStore(t)
	s.AddProvider(Provider{ID: "anthropic", Name: "Anthropic"})

	// Add two credentials with different priorities
	s.SetCredentials(Credentials{
		ID: "anthropic:backup", Provider: "anthropic", AuthType: "api_key",
		APIKey: "sk-backup", Priority: 200, Enabled: true,
	})
	s.SetCredentials(Credentials{
		ID: "anthropic:primary", Provider: "anthropic", AuthType: "api_key",
		APIKey: "sk-primary", Priority: 50, Enabled: true,
	})

	c, err := s.Resolve("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "anthropic:primary" {
		t.Errorf("expected primary (priority 50), got %q", c.ID)
	}
	if ActiveKey(c) != "sk-primary" {
		t.Errorf("expected sk-primary, got %q", ActiveKey(c))
	}

	// Disable primary — should fall back to backup
	s.db.Exec(`UPDATE credentials SET enabled = 0 WHERE id = 'anthropic:primary'`)
	c, err = s.Resolve("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "anthropic:backup" {
		t.Errorf("expected backup after disabling primary, got %q", c.ID)
	}
}

func TestResolveForModel(t *testing.T) {
	s := tempStore(t)
	s.AddProvider(Provider{ID: "anthropic", Name: "Anthropic"})
	s.AddModel(Model{ID: "claude-test", Provider: "anthropic", Name: "Test", Enabled: true, Priority: 10})

	// Provider-level credential
	s.SetCredentials(Credentials{
		ID: "anthropic:default", Provider: "anthropic", AuthType: "api_key",
		APIKey: "sk-provider", Priority: 100, Enabled: true,
	})
	// Model-specific credential
	s.SetCredentials(Credentials{
		ID: "anthropic:special", Provider: "anthropic", AuthType: "api_key",
		APIKey: "sk-special", Priority: 100, Enabled: true,
	})
	s.BindModelCredential("claude-test", "anthropic:special", 50)

	c, m, err := s.ResolveForModel("claude-test")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude-test" {
		t.Errorf("expected model claude-test, got %q", m.ID)
	}
	if c.ID != "anthropic:special" {
		t.Errorf("expected model-specific credential, got %q", c.ID)
	}

	// Remove binding — should fall back to provider
	s.UnbindModelCredential("claude-test", "anthropic:special")
	c, _, err = s.ResolveForModel("claude-test")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "anthropic:default" {
		t.Errorf("expected provider-level credential, got %q", c.ID)
	}
}

// mockOAuthProvider implements OAuthProvider for testing.
type mockOAuthProvider struct {
	id        string
	callCount int
	shouldErr bool
}

func (m *mockOAuthProvider) ID() string { return m.id }

func (m *mockOAuthProvider) RefreshToken(access, refresh string) (string, string, int64, error) {
	m.callCount++
	if m.shouldErr {
		return "", "", 0, fmt.Errorf("refresh failed")
	}
	newExpires := time.Now().Add(time.Hour).UnixMilli()
	return "new-access-token", refresh, newExpires, nil
}

func TestOAuthRefresh(t *testing.T) {
	s := tempStore(t)
	s.AddProvider(Provider{ID: "anthropic", Name: "Anthropic"})

	mock := &mockOAuthProvider{id: "anthropic"}
	s.RegisterOAuthProvider(mock)

	s.SetCredentials(Credentials{
		ID: "anthropic:oauth", Provider: "anthropic", AuthType: "oauth",
		Token: "old-token", RefreshToken: "refresh-token",
		ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(), // expired
		Priority: 100, Enabled: true,
	})

	c, err := s.Resolve("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 refresh call, got %d", mock.callCount)
	}
	if c.Token != "new-access-token" {
		t.Errorf("expected refreshed token, got %q", c.Token)
	}

	// Verify DB was updated
	creds, _ := s.ListCredentials("anthropic")
	if creds[0].Token != "new-access-token" {
		t.Error("DB not updated after refresh")
	}
}

func TestOAuthRefreshFallback(t *testing.T) {
	s := tempStore(t)
	s.AddProvider(Provider{ID: "anthropic", Name: "Anthropic"})

	mock := &mockOAuthProvider{id: "anthropic", shouldErr: true}
	s.RegisterOAuthProvider(mock)

	// Expired OAuth + working API key backup
	s.SetCredentials(Credentials{
		ID: "anthropic:oauth", Provider: "anthropic", AuthType: "oauth",
		Token: "expired-token", RefreshToken: "refresh",
		ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(),
		Priority: 50, Enabled: true,
	})
	s.SetCredentials(Credentials{
		ID: "anthropic:api", Provider: "anthropic", AuthType: "api_key",
		APIKey: "sk-backup", Priority: 100, Enabled: true,
	})

	c, err := s.Resolve("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	// Should fall back to API key since OAuth refresh failed
	if c.ID != "anthropic:api" {
		t.Errorf("expected fallback to api key, got %q", c.ID)
	}
}

func TestDeleteCredential(t *testing.T) {
	s := tempStore(t)
	s.AddProvider(Provider{ID: "test", Name: "Test"})
	s.SetCredentials(Credentials{
		ID: "test:1", Provider: "test", AuthType: "api_key",
		APIKey: "key1", Priority: 100, Enabled: true,
	})

	err := s.DeleteCredential("test:1")
	if err != nil {
		t.Fatal(err)
	}

	creds, _ := s.ListCredentials("test")
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials after delete, got %d", len(creds))
	}

	err = s.DeleteCredential("nonexistent")
	if err == nil {
		t.Error("expected error deleting nonexistent credential")
	}
}

func TestCredentialHealth(t *testing.T) {
	s := tempStore(t)
	s.AddProvider(Provider{ID: "test", Name: "Test"})
	s.SetCredentials(Credentials{
		ID: "test:1", Provider: "test", AuthType: "api_key",
		APIKey: "key1", Priority: 100, Enabled: true,
	})

	s.SetCredentialHealth("test:1", false, "timeout")
	creds, _ := s.ListCredentials("test")
	if creds[0].ErrorCount != 1 {
		t.Errorf("expected error_count 1, got %d", creds[0].ErrorCount)
	}

	s.SetCredentialHealth("test:1", true, "")
	creds, _ = s.ListCredentials("test")
	if creds[0].ErrorCount != 0 {
		t.Errorf("expected error_count 0 after success, got %d", creds[0].ErrorCount)
	}
}

func TestActiveKey(t *testing.T) {
	tests := []struct {
		cred *Credentials
		want string
	}{
		{&Credentials{AuthType: "api_key", APIKey: "sk-test"}, "sk-test"},
		{&Credentials{AuthType: "oauth", Token: "oat-token"}, "oat-token"},
		{&Credentials{AuthType: "token", Token: "bearer-token"}, "bearer-token"},
		{&Credentials{AuthType: "none"}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		got := ActiveKey(tt.cred)
		if got != tt.want {
			t.Errorf("ActiveKey(%v) = %q, want %q", tt.cred, got, tt.want)
		}
	}
}

func TestFreshDB(t *testing.T) {
	// Ensure Open works on a completely fresh path
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("DB file should exist")
	}

	// Should be able to seed and use
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}

	providers, _ := s.Providers()
	if len(providers) == 0 {
		t.Error("expected seeded providers")
	}
}
