package modelstore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// The two paginated providers' base endpoints live here so a test can point
// syncAnthropic and syncGoogle at a stand-in server and read back what actually
// went on the wire. Overriding one is a test-only affordance; nothing in the
// package writes them.
var (
	anthropicModelsEndpoint = "https://api.anthropic.com/v1/models"
	googleModelsEndpoint    = "https://generativelanguage.googleapis.com/v1beta/models"
)

// SyncResult holds the outcome of a provider sync.
type SyncResult struct {
	Provider string
	Added    int
	Updated  int
	Err      error
}

// SyncProvider fetches models from a provider's API and upserts them into the store.
// apiKey is the bearer token / API key for authentication.
func (s *Store) SyncProvider(provider, apiKey string) (*SyncResult, error) {
	switch provider {
	case "anthropic":
		return s.syncAnthropic(apiKey)
	case "openai":
		return s.syncOpenAI(apiKey)
	case "google":
		return s.syncGoogle(apiKey)
	default:
		return nil, fmt.Errorf("sync not supported for provider %q", provider)
	}
}

// --- Anthropic ---

type anthropicModelsResponse struct {
	Data    []anthropicModel `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

type anthropicModel struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	MaxInputTokens int    `json:"max_input_tokens"`
	MaxTokens      int    `json:"max_tokens"`
	CreatedAt      string `json:"created_at"`
	Type           string `json:"type"`
}

func (s *Store) syncAnthropic(apiKey string) (*SyncResult, error) {
	result := &SyncResult{Provider: "anthropic"}

	var allModels []anthropicModel
	afterID := ""

	for {
		// after_id is a value the upstream minted and handed back; it goes
		// through url.Values rather than concatenation so that a token
		// carrying "+", "&" or "=" stays one parameter with its own bytes.
		query := neturl.Values{"limit": {"100"}}
		if afterID != "" {
			query.Set("after_id", afterID)
		}
		requestURL := anthropicModelsEndpoint + "?" + query.Encode()

		req, err := http.NewRequest("GET", requestURL, nil)
		if err != nil {
			result.Err = err
			return result, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := httpClient().Do(req)
		if err != nil {
			result.Err = err
			return result, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			result.Err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncBody(body))
			return result, result.Err
		}

		var page anthropicModelsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			result.Err = err
			return result, err
		}

		allModels = append(allModels, page.Data...)

		if !page.HasMore || page.LastID == "" {
			break
		}
		afterID = page.LastID
	}

	for _, m := range allModels {
		existing, _ := s.ResolveModel(m.ID)
		model := Model{
			ID:        m.ID,
			Provider:  "anthropic",
			Name:      m.DisplayName,
			MaxTokens: m.MaxInputTokens,
			Enabled:   true,
		}
		if existing != nil {
			// Preserve user-set fields. AddModel replaces the whole row, so
			// anything not copied across here is erased by every sync run.
			model.Enabled = existing.Enabled
			model.Priority = existing.Priority
			model.InputCost = existing.InputCost
			model.OutputCost = existing.OutputCost
			model.Aliases = existing.Aliases
			model.ShortName = existing.ShortName
			// Update metadata from API
			model.Name = m.DisplayName
			model.MaxTokens = m.MaxInputTokens
			result.Updated++
		} else {
			model.Priority = 100 // new models get default priority
			model.InputCost, model.OutputCost = guessAnthropicCost(m.ID)
			result.Added++
		}
		if err := s.AddModel(model); err != nil {
			result.Err = err
			return result, err
		}
	}

	return result, nil
}

func guessAnthropicCost(id string) (input, output float64) {
	switch {
	case strings.Contains(id, "fable"):
		return 10.0, 50.0
	case strings.Contains(id, "opus"):
		return 5.0, 25.0
	case strings.Contains(id, "sonnet"):
		return 3.0, 15.0
	case strings.Contains(id, "haiku"):
		return 1.0, 5.0
	default:
		return 3.0, 15.0
	}
}

// --- OpenAI ---

type openaiModelsResponse struct {
	Data []openaiModel `json:"data"`
}

type openaiModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// openaiRelevant filters to chat/reasoning models useful for coding agents.
// Excludes audio, realtime, transcribe, tts, search, fine-tuned, and legacy snapshot variants.
func openaiRelevant(id string) bool {
	// Exclude non-chat models
	for _, exclude := range []string{"audio", "realtime", "transcribe", "tts", "search", "diarize"} {
		if strings.Contains(id, exclude) {
			return false
		}
	}
	// Exclude legacy GPT-3.5
	if strings.HasPrefix(id, "gpt-3.5") {
		return false
	}
	// Include known model families
	for _, prefix := range []string{"gpt-4", "gpt-5", "o1", "o3", "o4", "chatgpt-"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func (s *Store) syncOpenAI(apiKey string) (*SyncResult, error) {
	result := &SyncResult{Provider: "openai"}

	req, err := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
	if err != nil {
		result.Err = err
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient().Do(req)
	if err != nil {
		result.Err = err
		return result, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		result.Err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncBody(body))
		return result, result.Err
	}

	var page openaiModelsResponse
	if err := json.Unmarshal(body, &page); err != nil {
		result.Err = err
		return result, err
	}

	// Collect all IDs first to skip date-stamped snapshots when the base exists.
	idSet := map[string]bool{}
	for _, m := range page.Data {
		idSet[m.ID] = true
	}

	for _, m := range page.Data {
		if !openaiRelevant(m.ID) {
			continue
		}
		// Skip date-stamped snapshots (e.g. "gpt-4o-2024-08-06") when base model exists
		if isOpenAISnapshot(m.ID, idSet) {
			continue
		}
		existing, _ := s.ResolveModel(m.ID)
		model := Model{
			ID:       m.ID,
			Provider: "openai",
			Name:     m.ID,
			Enabled:  true,
		}
		if existing != nil {
			model.Enabled = existing.Enabled
			model.Priority = existing.Priority
			model.InputCost = existing.InputCost
			model.OutputCost = existing.OutputCost
			model.Aliases = existing.Aliases
			model.ShortName = existing.ShortName
			model.Name = existing.Name
			model.MaxTokens = existing.MaxTokens
			result.Updated++
		} else {
			model.Priority = 100
			model.MaxTokens, model.InputCost, model.OutputCost = guessOpenAICost(m.ID)
			result.Added++
		}
		if err := s.AddModel(model); err != nil {
			result.Err = err
			return result, err
		}
	}

	return result, nil
}

// isOpenAISnapshot returns true if the ID is a date-stamped snapshot (e.g. "gpt-4o-2024-08-06")
// and the base model (e.g. "gpt-4o") also exists in the set.
func isOpenAISnapshot(id string, idSet map[string]bool) bool {
	// Match patterns like "model-YYYY-MM-DD" or "model-YYYYMMDD"
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return false
	}
	// Try trimming date suffixes: "gpt-4o-2024-05-13" → check if "gpt-4o" exists
	// Date parts are 4-digit year or 2-digit month/day
	for i := len(parts) - 1; i >= 1; i-- {
		p := parts[i]
		isDatePart := len(p) >= 2 && len(p) <= 4 && p[0] >= '0' && p[0] <= '9'
		if !isDatePart {
			break
		}
		base := strings.Join(parts[:i], "-")
		if base != id && idSet[base] {
			return true
		}
	}
	return false
}

func guessOpenAICost(id string) (maxTokens int, input, output float64) {
	switch {
	case strings.HasPrefix(id, "o1-pro"):
		return 200000, 150.0, 600.0
	case strings.HasPrefix(id, "o3-mini"):
		return 200000, 1.10, 4.40
	case strings.HasPrefix(id, "o3"):
		return 200000, 2.0, 8.0
	case strings.HasPrefix(id, "o1"):
		return 200000, 15.0, 60.0
	case strings.HasPrefix(id, "o4-mini"):
		return 200000, 1.10, 4.40
	case strings.HasPrefix(id, "gpt-5-nano"):
		return 400000, 0.05, 0.40
	case strings.HasPrefix(id, "gpt-5-mini"):
		return 400000, 0.25, 2.00
	case strings.HasPrefix(id, "gpt-5-codex"):
		return 400000, 1.25, 10.0
	case strings.HasPrefix(id, "gpt-5"):
		return 400000, 1.25, 10.0
	case strings.HasPrefix(id, "gpt-4.1-nano"):
		return 1047576, 0.10, 0.40
	case strings.HasPrefix(id, "gpt-4.1-mini"):
		return 1047576, 0.40, 1.60
	case strings.HasPrefix(id, "gpt-4.1"):
		return 1047576, 2.0, 8.0
	case strings.HasPrefix(id, "gpt-4o-mini"):
		return 128000, 0.15, 0.60
	case strings.HasPrefix(id, "gpt-4o"):
		return 128000, 2.50, 10.0
	case strings.HasPrefix(id, "gpt-4-turbo"):
		return 128000, 10.0, 30.0
	case strings.HasPrefix(id, "gpt-4"):
		return 128000, 30.0, 60.0
	case strings.HasPrefix(id, "chatgpt-4o"):
		return 128000, 2.50, 10.0
	default:
		return 128000, 2.50, 10.0
	}
}

// --- Google ---

type googleModelsResponse struct {
	Models        []googleModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

type googleModel struct {
	Name                      string   `json:"name"`         // "models/gemini-2.5-pro"
	DisplayName               string   `json:"displayName"`
	InputTokenLimit           int      `json:"inputTokenLimit"`
	OutputTokenLimit          int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// googleRelevant filters to Gemini models that support generateContent.
func googleRelevant(m googleModel) bool {
	if !strings.Contains(m.Name, "gemini") {
		return false
	}
	for _, method := range m.SupportedGenerationMethods {
		if method == "generateContent" {
			return true
		}
	}
	return false
}

func (s *Store) syncGoogle(apiKey string) (*SyncResult, error) {
	result := &SyncResult{Provider: "google"}

	var allModels []googleModel
	pageToken := ""

	for {
		// key is a credential and pageToken is base64 the upstream minted;
		// both routinely carry "+", "/" and "=". A receiving server decodes a
		// raw "+" in a query string to a space, so concatenating either one
		// sends a value the holder never had, and the resulting failure names
		// nothing. url.Values escapes both.
		query := neturl.Values{"pageSize": {"100"}, "key": {apiKey}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		requestURL := googleModelsEndpoint + "?" + query.Encode()

		resp, err := httpClient().Get(requestURL)
		if err != nil {
			result.Err = err
			return result, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			result.Err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncBody(body))
			return result, result.Err
		}

		var page googleModelsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			result.Err = err
			return result, err
		}

		allModels = append(allModels, page.Models...)

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}

	for _, m := range allModels {
		if !googleRelevant(m) {
			continue
		}
		// Normalize ID: "models/gemini-2.5-pro" → "gemini-2.5-pro"
		id := strings.TrimPrefix(m.Name, "models/")

		existing, _ := s.ResolveModel(id)
		model := Model{
			ID:        id,
			Provider:  "google",
			Name:      m.DisplayName,
			MaxTokens: m.InputTokenLimit,
			Enabled:   true,
		}
		if existing != nil {
			model.Enabled = existing.Enabled
			model.Priority = existing.Priority
			model.InputCost = existing.InputCost
			model.OutputCost = existing.OutputCost
			model.Aliases = existing.Aliases
			model.ShortName = existing.ShortName
			model.Name = m.DisplayName
			model.MaxTokens = m.InputTokenLimit
			result.Updated++
		} else {
			model.Priority = 100
			model.InputCost, model.OutputCost = guessGoogleCost(id)
			result.Added++
		}
		if err := s.AddModel(model); err != nil {
			result.Err = err
			return result, err
		}
	}

	return result, nil
}

func guessGoogleCost(id string) (input, output float64) {
	switch {
	case strings.Contains(id, "flash"):
		return 0.15, 0.60
	case strings.Contains(id, "pro"):
		return 1.25, 10.0
	default:
		return 1.25, 10.0
	}
}

// --- helpers ---

func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func truncBody(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
