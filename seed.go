package modelstore

// Seed populates the store with known providers and models.
func (s *Store) Seed() error {
	providers := []Provider{
		{ID: "anthropic", Name: "Anthropic"},
		{ID: "openai", Name: "OpenAI"},
		{ID: "google", Name: "Google"},
		{ID: "ollama", Name: "Ollama (local)"},
		{ID: "openrouter", Name: "OpenRouter"},
	}

	for _, p := range providers {
		if err := s.AddProvider(p); err != nil {
			return err
		}
	}

	models := []Model{
		// Anthropic
		{ID: "claude-opus-4-6", Provider: "anthropic", Name: "Claude Opus 4.6",
			Aliases: []string{"opus", "claude-opus"}, MaxTokens: 200000, InputCost: 15.0, OutputCost: 75.0,
			Enabled: true, Priority: 10},
		{ID: "claude-sonnet-4-5-20250929", Provider: "anthropic", Name: "Claude Sonnet 4.5",
			Aliases: []string{"sonnet", "claude-sonnet"}, MaxTokens: 200000, InputCost: 3.0, OutputCost: 15.0,
			Enabled: true, Priority: 20},
		{ID: "claude-haiku-3-5-20241022", Provider: "anthropic", Name: "Claude Haiku 3.5",
			Aliases: []string{"haiku", "claude-haiku"}, MaxTokens: 200000, InputCost: 0.80, OutputCost: 4.0,
			Enabled: true, Priority: 30},

		// OpenAI
		{ID: "gpt-4o", Provider: "openai", Name: "GPT-4o",
			Aliases: []string{"4o"}, MaxTokens: 128000, InputCost: 2.50, OutputCost: 10.0,
			Enabled: false, Priority: 50},
		{ID: "gpt-4o-mini", Provider: "openai", Name: "GPT-4o Mini",
			Aliases: []string{"4o-mini", "mini"}, MaxTokens: 128000, InputCost: 0.15, OutputCost: 0.60,
			Enabled: false, Priority: 60},
		{ID: "o3", Provider: "openai", Name: "o3",
			Aliases: []string{"o3"}, MaxTokens: 200000, InputCost: 10.0, OutputCost: 40.0,
			Enabled: false, Priority: 40},

		// Google
		{ID: "gemini-2.5-pro", Provider: "google", Name: "Gemini 2.5 Pro",
			Aliases: []string{"gemini-pro", "gemini"}, MaxTokens: 1000000, InputCost: 1.25, OutputCost: 10.0,
			Enabled: false, Priority: 50},
		{ID: "gemini-2.5-flash", Provider: "google", Name: "Gemini 2.5 Flash",
			Aliases: []string{"gemini-flash", "flash"}, MaxTokens: 1000000, InputCost: 0.15, OutputCost: 0.60,
			Enabled: false, Priority: 70},
	}

	for _, m := range models {
		if err := s.AddModel(m); err != nil {
			return err
		}
	}

	return nil
}
