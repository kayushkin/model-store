package modelstore

import (
	"fmt"
	"time"
)

// AddProvider registers a provider.
func (s *Store) AddProvider(p Provider) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO providers (id, name, base_url, auth_type) VALUES (?, ?, ?, ?)`,
		p.ID, p.Name, p.BaseURL, p.AuthType,
	)
	return err
}

// Providers lists all registered providers.
func (s *Store) Providers() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT id, name, base_url, auth_type FROM providers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []Provider
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.AuthType); err != nil {
			continue
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// AddModel registers a model.
func (s *Store) AddModel(m Model) error {
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO models (id, provider, name, max_tokens, input_cost, output_cost, enabled, priority) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Provider, m.Name, m.MaxTokens, m.InputCost, m.OutputCost, enabled, m.Priority,
	)
	if err != nil {
		return err
	}
	// Add aliases
	for _, alias := range m.Aliases {
		s.db.Exec(`INSERT OR REPLACE INTO model_aliases (alias, model_id) VALUES (?, ?)`, alias, m.ID)
	}
	return nil
}

// Models lists models for a provider.
func (s *Store) Models(provider string) ([]Model, error) {
	rows, err := s.db.Query(
		`SELECT id, provider, name, max_tokens, input_cost, output_cost, enabled, priority FROM models WHERE provider = ? ORDER BY priority`,
		provider,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		var m Model
		var enabled int
		if err := rows.Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost, &enabled, &m.Priority); err != nil {
			continue
		}
		m.Enabled = enabled != 0
		// Load aliases
		aliasRows, _ := s.db.Query(`SELECT alias FROM model_aliases WHERE model_id = ?`, m.ID)
		if aliasRows != nil {
			for aliasRows.Next() {
				var a string
				aliasRows.Scan(&a)
				m.Aliases = append(m.Aliases, a)
			}
			aliasRows.Close()
		}
		models = append(models, m)
	}
	return models, nil
}

// ResolveModel resolves a model ID or alias to a full Model.
func (s *Store) ResolveModel(idOrAlias string) (*Model, error) {
	// Try direct ID first
	var m Model
	var enabled int
	err := s.db.QueryRow(
		`SELECT id, provider, name, max_tokens, input_cost, output_cost, enabled, priority FROM models WHERE id = ?`,
		idOrAlias,
	).Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost, &enabled, &m.Priority)

	if err != nil {
		// Try alias
		var modelID string
		err = s.db.QueryRow(`SELECT model_id FROM model_aliases WHERE alias = ?`, idOrAlias).Scan(&modelID)
		if err != nil {
			return nil, fmt.Errorf("model not found: %s", idOrAlias)
		}
		err = s.db.QueryRow(
			`SELECT id, provider, name, max_tokens, input_cost, output_cost, enabled, priority FROM models WHERE id = ?`,
			modelID,
		).Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost, &enabled, &m.Priority)
		if err != nil {
			return nil, fmt.Errorf("model not found: %s", modelID)
		}
	}

	m.Enabled = enabled != 0
	return &m, nil
}

// AllModelsWithStatus returns all models joined with their health data.
func (s *Store) AllModelsWithStatus() ([]ModelStatus, error) {
	s.migrateHealth()

	rows, err := s.db.Query(`
		SELECT m.id, m.provider, m.name, m.max_tokens, m.input_cost, m.output_cost, m.enabled, m.priority,
		       COALESCE(h.last_success_at, 0), COALESCE(h.last_success_ms, 0), COALESCE(h.avg_response_ms, 0),
		       COALESCE(h.last_error_at, 0), COALESCE(h.last_error, ''), COALESCE(h.consecutive_errors, 0)
		FROM models m
		LEFT JOIN model_health h ON m.id = h.model
		ORDER BY m.priority, m.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ModelStatus
	for rows.Next() {
		var ms ModelStatus
		var enabled int
		var successAt, errorAt int64
		var health ModelHealth

		if err := rows.Scan(
			&ms.ID, &ms.Provider, &ms.Name, &ms.MaxTokens, &ms.InputCost, &ms.OutputCost, &enabled, &ms.Priority,
			&successAt, &health.LastSuccessMs, &health.AvgResponseMs,
			&errorAt, &health.LastError, &health.ConsecutiveErr,
		); err != nil {
			continue
		}

		ms.Enabled = enabled != 0
		health.Model = ms.ID
		if successAt > 0 {
			health.LastSuccessAt = time.Unix(successAt, 0)
		}
		if errorAt > 0 {
			health.LastErrorAt = time.Unix(errorAt, 0)
		}
		ms.Health = &health
		result = append(result, ms)
	}
	return result, nil
}

// FailoverChain returns enabled models ordered by priority (for failover).
func (s *Store) FailoverChain() ([]Model, error) {
	rows, err := s.db.Query(`
		SELECT id, provider, name, max_tokens, input_cost, output_cost, enabled, priority
		FROM models WHERE enabled = 1 ORDER BY priority
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		var m Model
		var enabled int
		if err := rows.Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost, &enabled, &m.Priority); err != nil {
			continue
		}
		m.Enabled = true
		models = append(models, m)
	}
	return models, nil
}

// SetEnabled enables or disables a model.
func (s *Store) SetEnabled(modelID string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	res, err := s.db.Exec("UPDATE models SET enabled = ? WHERE id = ?", val, modelID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model %q not found", modelID)
	}
	return nil
}
