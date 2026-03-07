package modelstore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the central model/auth/usage registry.
type Store struct {
	db        *sql.DB
	path      string
	oauthProviders map[string]OAuthProvider
}

// OAuthProvider is the interface for refreshing OAuth tokens.
// Compatible with aiauth.Provider but doesn't import it directly —
// registration happens in oauth.go.
type OAuthProvider interface {
	ID() string
	RefreshToken(accessToken, refreshToken string) (newAccess, newRefresh string, expiresAtMs int64, err error)
}

// Provider represents a model provider (Anthropic, OpenAI, etc).
type Provider struct {
	ID   string `json:"id"`   // e.g. "anthropic", "openai", "google", "ollama"
	Name string `json:"name"` // display name
}

// Model represents an available model.
type Model struct {
	ID         string   `json:"id"`          // e.g. "claude-sonnet-4-5-20250929"
	Provider   string   `json:"provider"`    // provider ID
	Name       string   `json:"name"`        // display name
	Aliases    []string `json:"aliases"`     // e.g. ["sonnet", "claude-sonnet"]
	MaxTokens  int      `json:"max_tokens"`  // context window
	InputCost  float64  `json:"input_cost"`  // per million tokens
	OutputCost float64  `json:"output_cost"` // per million tokens
	Enabled    bool     `json:"enabled"`     // whether this model is available for use
	Priority   int      `json:"priority"`    // failover priority (lower = preferred, 0 = highest)
}

// ModelStatus combines model info with health data for dashboard display.
type ModelStatus struct {
	Model
	Health *ModelHealth `json:"health"`
}

// Credentials holds resolved auth for a provider/credential.
type Credentials struct {
	ID           string `json:"id"`                      // e.g. "anthropic:max", "anthropic:api"
	Provider     string `json:"provider"`
	Label        string `json:"label,omitempty"`          // human-readable description
	AuthType     string `json:"auth_type"`                // "api_key", "oauth", "token", "none"
	APIKey       string `json:"api_key,omitempty"`
	Token        string `json:"token,omitempty"`          // current access token for oauth/token
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`     // unix ms
	BaseURL      string `json:"base_url,omitempty"`       // per-credential API endpoint
	Priority     int    `json:"priority"`                 // lower = preferred
	Enabled      bool   `json:"enabled"`
	LastUsedAt   int64  `json:"last_used_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	LastErrorAt  int64  `json:"last_error_at,omitempty"`
	ErrorCount   int    `json:"error_count,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// IsExpired returns true if OAuth credentials need refreshing.
func (c *Credentials) IsExpired() bool {
	if c.AuthType != "oauth" || c.ExpiresAt == 0 {
		return false
	}
	now := time.Now().UnixMilli()
	return c.ExpiresAt < now
}

// UsageRecord tracks token usage.
type UsageRecord struct {
	Date         string  `json:"date"`          // YYYY-MM-DD
	Agent        string  `json:"agent"`         // "inber", "openclaw", "claude-code"
	Model        string  `json:"model"`         // model ID
	Provider     string  `json:"provider"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Requests     int     `json:"requests"`
	CostUSD      float64 `json:"cost_usd"`
}

// DefaultPath returns the default store path.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "model-store", "store.db")
}

// Open opens or creates a model store.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	os.MkdirAll(filepath.Dir(path), 0755)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	s := &Store{db: db, path: path, oauthProviders: make(map[string]OAuthProvider)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// Close closes the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// RegisterOAuthProvider registers a provider for OAuth token refresh.
func (s *Store) RegisterOAuthProvider(p OAuthProvider) {
	s.oauthProviders[p.ID()] = p
}

func (s *Store) migrate() error {
	// Create new schema tables
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS providers (
			id   TEXT PRIMARY KEY,
			name TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS models (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL REFERENCES providers(id),
			name TEXT NOT NULL,
			max_tokens INTEGER DEFAULT 200000,
			input_cost REAL DEFAULT 0,
			output_cost REAL DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			priority INTEGER DEFAULT 100
		);

		CREATE TABLE IF NOT EXISTS model_aliases (
			alias TEXT PRIMARY KEY,
			model_id TEXT NOT NULL REFERENCES models(id)
		);

		CREATE TABLE IF NOT EXISTS usage (
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
	`)
	if err != nil {
		return err
	}

	// Add columns to existing databases (safe to run multiple times)
	s.db.Exec(`ALTER TABLE models ADD COLUMN enabled INTEGER DEFAULT 1`)
	s.db.Exec(`ALTER TABLE models ADD COLUMN priority INTEGER DEFAULT 100`)

	// Migrate credentials from old schema to new if needed
	if err := s.migrateCredentials(); err != nil {
		return err
	}

	// Create model_credentials table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS model_credentials (
			model_id      TEXT NOT NULL REFERENCES models(id),
			credential_id TEXT NOT NULL REFERENCES credentials(id),
			priority      INTEGER DEFAULT 100,
			PRIMARY KEY (model_id, credential_id)
		);
	`)
	if err != nil {
		return err
	}

	// Drop old provider columns if they exist (safe — just ignore errors)
	// We can't DROP COLUMN in SQLite easily, so we leave them but ignore them.

	return nil
}

// migrateCredentials handles migration from old single-credential-per-provider to new multi-credential schema.
func (s *Store) migrateCredentials() error {
	// Check if new credentials table exists with 'id' column
	var cnt int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('credentials') WHERE name = 'id'`).Scan(&cnt)
	if err != nil {
		// Table might not exist at all
		cnt = 0
	}

	if cnt > 0 {
		// New schema already in place
		return nil
	}

	// Check if old credentials table exists
	var oldExists int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='credentials'`).Scan(&oldExists)

	if oldExists > 0 {
		// Migrate old → new
		// Read old data
		rows, err := s.db.Query(`SELECT provider, auth_type, api_key, token, refresh_token, COALESCE(expires_at, 0), base_url FROM credentials`)
		if err != nil {
			return fmt.Errorf("reading old credentials: %w", err)
		}

		type oldCred struct {
			provider, authType, apiKey, token, refreshToken string
			expiresAt                                       int64
			baseURL                                         string
		}
		var oldCreds []oldCred
		for rows.Next() {
			var c oldCred
			var apiKey, token, refreshToken, baseURL sql.NullString
			if err := rows.Scan(&c.provider, &c.authType, &apiKey, &token, &refreshToken, &c.expiresAt, &baseURL); err != nil {
				continue
			}
			c.apiKey = apiKey.String
			c.token = token.String
			c.refreshToken = refreshToken.String
			c.baseURL = baseURL.String
			oldCreds = append(oldCreds, c)
		}
		rows.Close()

		// Rename old table
		_, err = s.db.Exec(`ALTER TABLE credentials RENAME TO credentials_old`)
		if err != nil {
			return fmt.Errorf("renaming old credentials: %w", err)
		}

		// Create new table
		if err := s.createCredentialsTable(); err != nil {
			return err
		}

		// Migrate data
		for _, c := range oldCreds {
			// Convert old expires_at (unix seconds) to unix ms
			expiresMs := c.expiresAt
			if expiresMs > 0 && expiresMs < 1e12 {
				// Looks like seconds, convert to ms
				expiresMs = expiresMs * 1000
			}
			id := c.provider + ":default"
			_, err := s.db.Exec(`
				INSERT OR REPLACE INTO credentials (id, provider, label, auth_type, api_key, token, refresh_token, expires_at, base_url, priority, enabled)
				VALUES (?, ?, 'Migrated default', ?, ?, ?, ?, ?, ?, 100, 1)`,
				id, c.provider, c.authType, c.apiKey, c.token, c.refreshToken, expiresMs, c.baseURL,
			)
			if err != nil {
				return fmt.Errorf("migrating credential %s: %w", id, err)
			}
		}
	} else {
		// No old table — just create new
		if err := s.createCredentialsTable(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) createCredentialsTable() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS credentials (
			id            TEXT PRIMARY KEY,
			provider      TEXT NOT NULL REFERENCES providers(id),
			label         TEXT DEFAULT '',
			auth_type     TEXT NOT NULL,
			api_key       TEXT,
			token         TEXT,
			refresh_token TEXT,
			expires_at    INTEGER DEFAULT 0,
			base_url      TEXT,
			priority      INTEGER DEFAULT 100,
			enabled       INTEGER DEFAULT 1,
			last_used_at  INTEGER DEFAULT 0,
			last_error    TEXT DEFAULT '',
			last_error_at INTEGER DEFAULT 0,
			error_count   INTEGER DEFAULT 0,
			created_at    TEXT DEFAULT (datetime('now'))
		);
	`)
	return err
}
