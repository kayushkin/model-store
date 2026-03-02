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
	db   *sql.DB
	path string
}

// Provider represents a model provider (Anthropic, OpenAI, etc).
type Provider struct {
	ID       string `json:"id"`       // e.g. "anthropic", "openai", "google", "ollama"
	Name     string `json:"name"`     // display name
	BaseURL  string `json:"base_url"` // API base URL
	AuthType string `json:"auth_type"` // "api_key", "oauth", "none"
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
}

// Credentials holds resolved auth for a provider.
type Credentials struct {
	Provider string `json:"provider"`
	AuthType string `json:"auth_type"` // "api_key", "oauth", "none"
	APIKey   string `json:"api_key,omitempty"`
	Token    string `json:"token,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	// OAuth fields
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
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

	s := &Store{db: db, path: path}
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

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS providers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			base_url TEXT,
			auth_type TEXT NOT NULL DEFAULT 'api_key'
		);

		CREATE TABLE IF NOT EXISTS models (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL REFERENCES providers(id),
			name TEXT NOT NULL,
			max_tokens INTEGER DEFAULT 200000,
			input_cost REAL DEFAULT 0,
			output_cost REAL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS model_aliases (
			alias TEXT PRIMARY KEY,
			model_id TEXT NOT NULL REFERENCES models(id)
		);

		CREATE TABLE IF NOT EXISTS credentials (
			provider TEXT PRIMARY KEY REFERENCES providers(id),
			auth_type TEXT NOT NULL,
			api_key TEXT,
			token TEXT,
			refresh_token TEXT,
			expires_at INTEGER,
			base_url TEXT
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
	return err
}
