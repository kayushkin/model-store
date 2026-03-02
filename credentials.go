package modelstore

import (
	"fmt"
	"time"
)

// SetCredentials stores credentials for a provider.
func (s *Store) SetCredentials(c Credentials) error {
	var expiresAt int64
	if !c.ExpiresAt.IsZero() {
		expiresAt = c.ExpiresAt.Unix()
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO credentials (provider, auth_type, api_key, token, refresh_token, expires_at, base_url)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Provider, c.AuthType, c.APIKey, c.Token, c.RefreshToken, expiresAt, c.BaseURL,
	)
	return err
}

// Resolve returns credentials for a provider.
func (s *Store) Resolve(provider string) (*Credentials, error) {
	var c Credentials
	var expiresAt int64
	err := s.db.QueryRow(
		`SELECT provider, auth_type, api_key, token, refresh_token, expires_at, base_url
		 FROM credentials WHERE provider = ?`,
		provider,
	).Scan(&c.Provider, &c.AuthType, &c.APIKey, &c.Token, &c.RefreshToken, &expiresAt, &c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("no credentials for provider %s: %w", provider, err)
	}
	if expiresAt > 0 {
		c.ExpiresAt = time.Unix(expiresAt, 0)
	}
	return &c, nil
}

// ResolveForModel resolves credentials for a model (by ID or alias).
func (s *Store) ResolveForModel(modelIDOrAlias string) (*Credentials, *Model, error) {
	m, err := s.ResolveModel(modelIDOrAlias)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.Resolve(m.Provider)
	if err != nil {
		return nil, m, err
	}
	return c, m, nil
}

// IsExpired returns true if OAuth credentials need refreshing.
func (c *Credentials) IsExpired() bool {
	if c.AuthType != "oauth" || c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt.Add(-5 * time.Minute)) // 5 min buffer
}
