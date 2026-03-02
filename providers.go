package modelstore

import "fmt"

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
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO models (id, provider, name, max_tokens, input_cost, output_cost) VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.Provider, m.Name, m.MaxTokens, m.InputCost, m.OutputCost,
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
		`SELECT id, provider, name, max_tokens, input_cost, output_cost FROM models WHERE provider = ?`,
		provider,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost); err != nil {
			continue
		}
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
	err := s.db.QueryRow(
		`SELECT id, provider, name, max_tokens, input_cost, output_cost FROM models WHERE id = ?`,
		idOrAlias,
	).Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost)

	if err != nil {
		// Try alias
		var modelID string
		err = s.db.QueryRow(`SELECT model_id FROM model_aliases WHERE alias = ?`, idOrAlias).Scan(&modelID)
		if err != nil {
			return nil, fmt.Errorf("model not found: %s", idOrAlias)
		}
		err = s.db.QueryRow(
			`SELECT id, provider, name, max_tokens, input_cost, output_cost FROM models WHERE id = ?`,
			modelID,
		).Scan(&m.ID, &m.Provider, &m.Name, &m.MaxTokens, &m.InputCost, &m.OutputCost)
		if err != nil {
			return nil, fmt.Errorf("model not found: %s", modelID)
		}
	}

	return &m, nil
}
