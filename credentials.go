package modelstore

import (
	"database/sql"
	"fmt"
	"time"
)

// SetCredentials stores or updates a credential.
func (s *Store) SetCredentials(c Credentials) error {
	if c.ID == "" {
		return fmt.Errorf("credential ID is required")
	}
	if c.Provider == "" {
		return fmt.Errorf("credential provider is required")
	}
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO credentials
			(id, provider, label, auth_type, api_key, token, refresh_token, expires_at, base_url, priority, enabled, last_used_at, last_error, last_error_at, error_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Provider, c.Label, c.AuthType, c.APIKey, c.Token, c.RefreshToken,
		c.ExpiresAt, c.BaseURL, c.Priority, enabled,
		c.LastUsedAt, c.LastError, c.LastErrorAt, c.ErrorCount,
	)
	if err == nil {
		s.autoSync()
	}
	return err
}

// Resolve returns the best available credential for a provider.
// For OAuth credentials that are expired, attempts to refresh before returning.
func (s *Store) Resolve(provider string) (*Credentials, error) {
	rows, err := s.db.Query(`
		SELECT id, provider, label, auth_type, api_key, token, refresh_token,
		       expires_at, base_url, priority, enabled, last_used_at,
		       last_error, last_error_at, error_count, created_at
		FROM credentials
		WHERE provider = ? AND enabled = 1
		ORDER BY priority ASC, error_count ASC`,
		provider,
	)
	if err != nil {
		return nil, fmt.Errorf("querying credentials for %s: %w", provider, err)
	}
	defer rows.Close()

	var candidates []*Credentials
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			continue
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	for _, c := range candidates {
		// If OAuth and expired, try to refresh
		if c.IsExpired() {
			if refreshErr := s.refreshOAuthCredential(c); refreshErr != nil {
				s.SetCredentialHealth(c.ID, false, refreshErr.Error())
				continue
			}
		}

		// Update last_used_at
		now := time.Now().UnixMilli()
		s.db.Exec(`UPDATE credentials SET last_used_at = ? WHERE id = ?`, now, c.ID)

		return c, nil
	}

	return nil, fmt.Errorf("no credentials for provider %s", provider)
}

// ResolveForModel resolves credentials for a model (by ID or alias).
// Checks model_credentials first for model-specific bindings, then falls back to provider-level.
func (s *Store) ResolveForModel(modelIDOrAlias string) (*Credentials, *Model, error) {
	m, err := s.ResolveModel(modelIDOrAlias)
	if err != nil {
		return nil, nil, err
	}

	// Check for model-specific credential bindings
	rows, err := s.db.Query(`
		SELECT c.id, c.provider, c.label, c.auth_type, c.api_key, c.token, c.refresh_token,
		       c.expires_at, c.base_url, c.priority, c.enabled, c.last_used_at,
		       c.last_error, c.last_error_at, c.error_count, c.created_at
		FROM credentials c
		JOIN model_credentials mc ON c.id = mc.credential_id
		WHERE mc.model_id = ? AND c.enabled = 1
		ORDER BY mc.priority ASC, c.error_count ASC`,
		m.ID,
	)
	if err == nil {
		var modelCandidates []*Credentials
		for rows.Next() {
			c, scanErr := scanCredential(rows)
			if scanErr != nil {
				continue
			}
			modelCandidates = append(modelCandidates, c)
		}
		rows.Close()

		for _, c := range modelCandidates {
			if c.IsExpired() {
				if refreshErr := s.refreshOAuthCredential(c); refreshErr != nil {
					s.SetCredentialHealth(c.ID, false, refreshErr.Error())
					continue
				}
			}
			now := time.Now().UnixMilli()
			s.db.Exec(`UPDATE credentials SET last_used_at = ? WHERE id = ?`, now, c.ID)
			return c, m, nil
		}
	}

	// Fall back to provider-level
	c, err := s.Resolve(m.Provider)
	if err != nil {
		return nil, m, err
	}
	return c, m, nil
}

// ListCredentials lists all credentials for a provider (or all if provider is empty).
func (s *Store) ListCredentials(provider string) ([]Credentials, error) {
	var query string
	var args []interface{}
	if provider != "" {
		query = `SELECT id, provider, label, auth_type, api_key, token, refresh_token,
		         expires_at, base_url, priority, enabled, last_used_at,
		         last_error, last_error_at, error_count, created_at
		         FROM credentials WHERE provider = ? ORDER BY priority ASC`
		args = []interface{}{provider}
	} else {
		query = `SELECT id, provider, label, auth_type, api_key, token, refresh_token,
		         expires_at, base_url, priority, enabled, last_used_at,
		         last_error, last_error_at, error_count, created_at
		         FROM credentials ORDER BY provider, priority ASC`
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []Credentials
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			continue
		}
		creds = append(creds, *c)
	}
	return creds, nil
}

// DeleteCredential removes a credential by ID.
func (s *Store) DeleteCredential(id string) error {
	// Remove model bindings first
	s.db.Exec(`DELETE FROM model_credentials WHERE credential_id = ?`, id)

	res, err := s.db.Exec(`DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("credential %q not found", id)
	}
	return nil
}

// SetCredentialEnabled enables or disables a credential by ID.
func (s *Store) SetCredentialEnabled(id string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	res, err := s.db.Exec(`UPDATE credentials SET enabled = ? WHERE id = ?`, enabledInt, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("credential %q not found", id)
	}
	s.autoSync()
	return nil
}

// SetCredentialHealth updates health tracking fields for a credential.
func (s *Store) SetCredentialHealth(id string, success bool, errMsg string) {
	now := time.Now().UnixMilli()
	if success {
		s.db.Exec(`UPDATE credentials SET last_error = '', error_count = 0, last_used_at = ? WHERE id = ?`, now, id)
	} else {
		s.db.Exec(`UPDATE credentials SET last_error = ?, last_error_at = ?, error_count = error_count + 1 WHERE id = ?`,
			errMsg, now, id)
	}
}

// ActiveKey returns the usable key/token from a credential based on its auth_type.
func ActiveKey(cred *Credentials) string {
	if cred == nil {
		return ""
	}
	switch cred.AuthType {
	case "api_key":
		return cred.APIKey
	case "oauth":
		return cred.Token // OAuth access token stored in token field
	case "token":
		return cred.Token
	default:
		return ""
	}
}

// CredentialsForModel returns the credentials available for a specific model.
// Checks model_credentials bindings first; if none, falls back to all provider-level credentials.
// Does NOT filter by enabled — returns all so the dashboard can show full picture.
func (s *Store) CredentialsForModel(modelID string) ([]Credentials, error) {
	// First check for model-specific bindings
	rows, err := s.db.Query(`
		SELECT c.id, c.provider, c.label, c.auth_type, c.api_key, c.token, c.refresh_token,
		       c.expires_at, c.base_url, c.priority, c.enabled, c.last_used_at,
		       c.last_error, c.last_error_at, c.error_count, c.created_at
		FROM credentials c
		JOIN model_credentials mc ON c.id = mc.credential_id
		WHERE mc.model_id = ?
		ORDER BY mc.priority ASC, c.priority ASC`,
		modelID,
	)
	if err != nil {
		return nil, err
	}

	var creds []Credentials
	for rows.Next() {
		c, scanErr := scanCredential(rows)
		if scanErr != nil {
			continue
		}
		creds = append(creds, *c)
	}
	rows.Close()

	if len(creds) > 0 {
		return creds, nil
	}

	// Fall back to provider-level: look up the model's provider, then list all creds for it
	var provider string
	err = s.db.QueryRow(`SELECT provider FROM models WHERE id = ?`, modelID).Scan(&provider)
	if err != nil {
		return nil, fmt.Errorf("model %q not found", modelID)
	}

	return s.ListCredentials(provider)
}

// BindModelCredential creates a model-specific credential binding.
func (s *Store) BindModelCredential(modelID, credentialID string, priority int) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO model_credentials (model_id, credential_id, priority)
		VALUES (?, ?, ?)`, modelID, credentialID, priority)
	return err
}

// UnbindModelCredential removes a model-specific credential binding.
func (s *Store) UnbindModelCredential(modelID, credentialID string) error {
	_, err := s.db.Exec(`DELETE FROM model_credentials WHERE model_id = ? AND credential_id = ?`,
		modelID, credentialID)
	return err
}

// refreshOAuthCredential attempts to refresh an expired OAuth credential using registered providers.
func (s *Store) refreshOAuthCredential(c *Credentials) error {
	p, ok := s.oauthProviders[c.Provider]
	if !ok {
		return fmt.Errorf("no OAuth provider registered for %s", c.Provider)
	}

	newAccess, newRefresh, expiresMs, err := p.RefreshToken(c.Token, c.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh failed for %s: %w", c.ID, err)
	}

	c.Token = newAccess
	if newRefresh != "" {
		c.RefreshToken = newRefresh
	}
	c.ExpiresAt = expiresMs

	// Update DB
	_, err = s.db.Exec(`UPDATE credentials SET token = ?, refresh_token = ?, expires_at = ?, last_error = '', error_count = 0 WHERE id = ?`,
		c.Token, c.RefreshToken, c.ExpiresAt, c.ID)
	if err == nil {
		s.autoSync()
	}
	return err
}

// scanCredential scans a credential row from a *sql.Rows.
func scanCredential(rows *sql.Rows) (*Credentials, error) {
	var c Credentials
	var enabled int
	var apiKey, token, refreshToken, baseURL, label, lastError, createdAt sql.NullString
	var expiresAt, lastUsedAt, lastErrorAt sql.NullInt64
	var errorCount sql.NullInt64

	err := rows.Scan(
		&c.ID, &c.Provider, &label, &c.AuthType, &apiKey, &token, &refreshToken,
		&expiresAt, &baseURL, &c.Priority, &enabled, &lastUsedAt,
		&lastError, &lastErrorAt, &errorCount, &createdAt,
	)
	if err != nil {
		return nil, err
	}

	c.Label = label.String
	c.APIKey = apiKey.String
	c.Token = token.String
	c.RefreshToken = refreshToken.String
	c.ExpiresAt = expiresAt.Int64
	c.BaseURL = baseURL.String
	c.Enabled = enabled != 0
	c.LastUsedAt = lastUsedAt.Int64
	c.LastError = lastError.String
	c.LastErrorAt = lastErrorAt.Int64
	c.ErrorCount = int(errorCount.Int64)
	c.CreatedAt = createdAt.String

	return &c, nil
}
