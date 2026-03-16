package modelstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// authProfileStore is the on-disk format for OpenClaw's auth-profiles.json.
type authProfileStore struct {
	Version    int                               `json:"version"`
	Profiles   map[string]*authProfileCredential `json:"profiles"`
	LastGood   map[string]string                 `json:"lastGood,omitempty"`
	UsageStats map[string]json.RawMessage        `json:"usageStats,omitempty"`
}

// authProfileCredential is a single credential in auth-profiles.json.
type authProfileCredential struct {
	Type     string `json:"type"`               // "api_key", "token", "oauth"
	Provider string `json:"provider"`
	Key      string `json:"key,omitempty"`       // for api_key
	Token    string `json:"token,omitempty"`     // for token type
	Access   string `json:"access,omitempty"`    // for oauth
	Refresh  string `json:"refresh,omitempty"`   // for oauth
	Expires  int64  `json:"expires,omitempty"`   // unix ms
	Email    string `json:"email,omitempty"`
}

// DefaultAuthProfilesPath returns the default OpenClaw auth-profiles.json path.
func DefaultAuthProfilesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".openclaw", "agents", "main", "agent", "auth-profiles.json")
}

// SyncToAuthProfiles writes all credentials to an OpenClaw auth-profiles.json file.
// This keeps OpenClaw in sync with model-store (the source of truth).
// Preserves existing usageStats from the file.
func (s *Store) SyncToAuthProfiles(path string) error {
	if path == "" {
		path = DefaultAuthProfilesPath()
	}

	// Read existing file to preserve usageStats
	var existing authProfileStore
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &existing)
	}

	// Build new profiles from model-store credentials
	creds, err := s.ListCredentials("")
	if err != nil {
		return fmt.Errorf("listing credentials: %w", err)
	}

	profiles := make(map[string]*authProfileCredential)
	lastGood := make(map[string]string)

	// Track best (lowest priority, enabled) credential per provider for lastGood
	type bestEntry struct {
		priority int
		id       string
	}
	best := make(map[string]*bestEntry)

	for _, c := range creds {
		profile := &authProfileCredential{
			Provider: c.Provider,
		}

		switch c.AuthType {
		case "api_key":
			profile.Type = "api_key"
			profile.Key = c.APIKey
		case "oauth":
			profile.Type = "oauth"
			profile.Access = c.Token
			profile.Refresh = c.RefreshToken
			profile.Expires = c.ExpiresAt
		case "token":
			profile.Type = "token"
			profile.Token = c.Token
			profile.Expires = c.ExpiresAt
		default:
			continue
		}

		profiles[c.ID] = profile

		// Also create a compat "manual" token entry for OAuth (OpenClaw expects this)
		if c.AuthType == "oauth" && c.Token != "" {
			manualID := c.Provider + ":manual"
			profiles[manualID] = &authProfileCredential{
				Type:     "token",
				Provider: c.Provider,
				Token:    c.Token,
				Expires:  c.ExpiresAt,
			}
		}

		// Track best credential per provider
		if c.Enabled {
			if b, ok := best[c.Provider]; !ok || c.Priority < b.priority {
				best[c.Provider] = &bestEntry{priority: c.Priority, id: c.ID}
			}
		}
	}

	// Set lastGood per provider
	for provider, b := range best {
		// OpenClaw prefers the "manual" token for OAuth providers
		if profiles[b.id] != nil && profiles[b.id].Type == "oauth" {
			manualID := provider + ":manual"
			if profiles[manualID] != nil {
				lastGood[provider] = manualID
			} else {
				lastGood[provider] = b.id
			}
		} else {
			lastGood[provider] = b.id
		}
	}

	store := &authProfileStore{
		Version:    1,
		Profiles:   profiles,
		LastGood:   lastGood,
		UsageStats: existing.UsageStats, // preserve existing stats
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling auth profiles: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating dir: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// SyncFromAuthProfiles reads auth-profiles.json and updates OAuth/token credentials
// in the model-store DB. Only updates credentials that already exist (by matching
// provider + auth_type). This is the reverse of SyncToAuthProfiles.
func (s *Store) SyncFromAuthProfiles(path string) (int, error) {
	if path == "" {
		path = DefaultAuthProfilesPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading auth profiles: %w", err)
	}

	var store authProfileStore
	if err := json.Unmarshal(data, &store); err != nil {
		return 0, fmt.Errorf("parsing auth profiles: %w", err)
	}

	// Get existing credentials from DB
	existing, err := s.ListCredentials("")
	if err != nil {
		return 0, fmt.Errorf("listing credentials: %w", err)
	}

	updated := 0

	for profileID, profile := range store.Profiles {
		if profile == nil {
			continue
		}

		// Try to match by profile ID first (e.g. "anthropic:max-oauth")
		var target *Credentials
		for i := range existing {
			if existing[i].ID == profileID {
				target = &existing[i]
				break
			}
		}

		// If no exact ID match, match OAuth profiles to existing OAuth creds by provider
		if target == nil && profile.Type == "oauth" {
			for i := range existing {
				if existing[i].Provider == profile.Provider && existing[i].AuthType == "oauth" {
					target = &existing[i]
					break
				}
			}
		}

		if target == nil {
			continue // don't create new credentials, only update existing
		}

		// Check if the auth-profiles version is newer (different token)
		switch profile.Type {
		case "oauth":
			if profile.Access != "" && profile.Access != target.Token {
				target.Token = profile.Access
				if profile.Refresh != "" {
					target.RefreshToken = profile.Refresh
				}
				target.ExpiresAt = profile.Expires
				target.ErrorCount = 0
				target.LastError = ""
				if err := s.SetCredentials(*target); err != nil {
					continue
				}
				updated++
			}
		case "token":
			if target.AuthType == "oauth" {
				// Skip manual/token profiles for OAuth credentials
				// (these are derived from OAuth, not the source of truth)
				continue
			}
			if profile.Token != "" && profile.Token != target.Token {
				target.Token = profile.Token
				target.ExpiresAt = profile.Expires
				target.ErrorCount = 0
				target.LastError = ""
				if err := s.SetCredentials(*target); err != nil {
					continue
				}
				updated++
			}
		}
	}

	return updated, nil
}
