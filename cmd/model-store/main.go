// model-store: HTTP API for shared model/credential management.
// Dashboard and orchestrators query this for model status, health,
// and credential management.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kayushkin/bus"
	"github.com/kayushkin/bus/messages"
	modelstore "github.com/kayushkin/model-store"
)

func main() {
	addr := flag.String("addr", ":8150", "listen address")
	dbPath := flag.String("db", "", "model-store DB path (default: ~/.config/model-store/store.db)")
	flag.Parse()

	log.Printf("model-store starting on %s", *addr)

	mux := http.NewServeMux()

	// Open store once to validate, then use per-request opens for write safety.
	store, err := modelstore.Open(*dbPath)
	if err != nil {
		log.Fatalf("failed to open model store: %v", err)
	}
	store.Close()

	// Helper: open store with OAuth providers registered (for auto-refresh)
	openStore := func() (*modelstore.Store, error) {
		s, err := modelstore.Open(*dbPath)
		if err != nil {
			return nil, err
		}
		s.RegisterDefaultOAuthProviders()
		s.EnableAuthProfileSync("")
		return s, nil
	}
	// Background OAuth token health check — refresh tokens before they expire.
	go func() {
		// Initial delay, then check every 30 minutes.
		time.Sleep(10 * time.Second)
		for {
			s, err := openStore()
			if err != nil {
				log.Printf("[oauth-refresh] failed to open store: %v", err)
			} else {
				creds, _ := s.ListCredentials("")
				now := time.Now().UnixMilli()
				for _, c := range creds {
					if c.AuthType != "oauth" || !c.Enabled {
						continue
					}
					// Refresh if expired or expiring within 1 hour
					if c.ExpiresAt > 0 && c.ExpiresAt-now < 3600_000 {
						log.Printf("[oauth-refresh] %s expires in %dm, refreshing...", c.ID, (c.ExpiresAt-now)/60_000)
						if err := s.RefreshOAuthCredential(c.ID); err != nil {
							log.Printf("[oauth-refresh] %s refresh failed: %v", c.ID, err)
						} else {
							log.Printf("[oauth-refresh] %s refreshed successfully", c.ID)
						}
					}
				}
				s.Close()
			}
			time.Sleep(30 * time.Minute)
		}
	}()

	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		store, err := modelstore.Open(*dbPath)
		if err != nil {
			httpError(w, "failed to open model store", http.StatusInternalServerError)
			return
		}
		defer store.Close()

		statuses, err := store.AllModelsWithStatus()
		if err != nil {
			httpError(w, "failed to query models", http.StatusInternalServerError)
			return
		}

		type credRef struct {
			ID           string `json:"id"`
			Label        string `json:"label"`
			AuthType     string `json:"auth_type"`
			APIKeyMasked string `json:"api_key_masked,omitempty"`
			TokenMasked  string `json:"token_masked,omitempty"`
			Priority     int    `json:"priority"`
			Enabled      bool   `json:"enabled"`
			LastUsedAt   int64  `json:"last_used_at"`
			ErrorCount   int    `json:"error_count"`
			ExpiresAt    int64  `json:"expires_at"`
		}
		type modelWithCreds struct {
			modelstore.ModelStatus
			Credentials []credRef `json:"credentials"`
		}

		var result []modelWithCreds
		for _, ms := range statuses {
			mc := modelWithCreds{ModelStatus: ms}
			creds, err := store.CredentialsForModel(ms.ID)
			if err == nil {
				for _, c := range creds {
					cr := credRef{
						ID:         c.ID,
						Label:      c.Label,
						AuthType:   c.AuthType,
						Priority:   c.Priority,
						Enabled:    c.Enabled,
						LastUsedAt: c.LastUsedAt,
						ErrorCount: c.ErrorCount,
						ExpiresAt:  c.ExpiresAt,
					}
					if c.APIKey != "" {
						cr.APIKeyMasked = maskKey(c.APIKey)
					}
					if c.Token != "" {
						cr.TokenMasked = maskKey(c.Token)
					}
					mc.Credentials = append(mc.Credentials, cr)
				}
			}
			if mc.Credentials == nil {
				mc.Credentials = []credRef{}
			}
			result = append(result, mc)
		}

		jsonResponse(w, result)
	})

	mux.HandleFunc("/api/models/toggle", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Model   string `json:"model"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
			httpError(w, "model required", http.StatusBadRequest)
			return
		}

		store, err := modelstore.Open(*dbPath)
		if err != nil {
			httpError(w, "failed to open model store", http.StatusInternalServerError)
			return
		}
		defer store.Close()

		if err := store.SetEnabled(req.Model, req.Enabled); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		jsonResponse(w, map[string]any{"ok": true, "model": req.Model, "enabled": req.Enabled})
	})

	mux.HandleFunc("/api/models/test", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
			httpError(w, "model required", http.StatusBadRequest)
			return
		}

		// Use inber CLI to test model.
		inberBin := findBinary("inber")
		if inberBin == "" {
			httpError(w, "inber binary not found", http.StatusInternalServerError)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, inberBin, "run", "--raw", "--no-tools", "--new", "--detach", "-m", req.Model, "Reply with just: ok")
		cmd.Env = os.Environ()

		start := time.Now()
		output, err := cmd.CombinedOutput()
		durationMs := time.Since(start).Milliseconds()

		result := map[string]any{
			"model":       req.Model,
			"duration_ms": durationMs,
		}

		if err != nil {
			result["status"] = "error"
			errText := string(output)
			if len(errText) > 200 {
				errText = errText[len(errText)-200:]
			}
			result["error"] = errText
		} else {
			result["status"] = "ok"
			text := strings.TrimSpace(string(output))
			if len(text) > 100 {
				text = text[:100]
			}
			result["response"] = text
		}

		jsonResponse(w, result)
	})

	mux.HandleFunc("/api/credentials", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		store, err := modelstore.Open(*dbPath)
		if err != nil {
			httpError(w, "failed to open model store", http.StatusInternalServerError)
			return
		}
		defer store.Close()

		creds, err := store.ListCredentials("")
		if err != nil {
			httpError(w, "failed to query credentials", http.StatusInternalServerError)
			return
		}

		type credResponse struct {
			ID           string `json:"id"`
			Provider     string `json:"provider"`
			Label        string `json:"label"`
			AuthType     string `json:"auth_type"`
			APIKeyMasked string `json:"api_key_masked,omitempty"`
			TokenMasked  string `json:"token_masked,omitempty"`
			BaseURL      string `json:"base_url,omitempty"`
			Priority     int    `json:"priority"`
			Enabled      bool   `json:"enabled"`
			LastUsedAt   int64  `json:"last_used_at"`
			LastError    string `json:"last_error,omitempty"`
			LastErrorAt  int64  `json:"last_error_at"`
			ErrorCount   int    `json:"error_count"`
			ExpiresAt    int64  `json:"expires_at"`
			CreatedAt    string `json:"created_at"`
		}

		var response []credResponse
		for _, c := range creds {
			cr := credResponse{
				ID:          c.ID,
				Provider:    c.Provider,
				Label:       c.Label,
				AuthType:    c.AuthType,
				BaseURL:     c.BaseURL,
				Priority:    c.Priority,
				Enabled:     c.Enabled,
				LastUsedAt:  c.LastUsedAt,
				LastError:   c.LastError,
				LastErrorAt: c.LastErrorAt,
				ErrorCount:  c.ErrorCount,
				ExpiresAt:   c.ExpiresAt,
				CreatedAt:   c.CreatedAt,
			}
			if c.APIKey != "" {
				cr.APIKeyMasked = maskKey(c.APIKey)
			}
			if c.Token != "" {
				cr.TokenMasked = maskKey(c.Token)
			}
			response = append(response, cr)
		}
		if response == nil {
			response = []credResponse{}
		}

		jsonResponse(w, response)
	})

	mux.HandleFunc("/api/credentials/toggle", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			httpError(w, "id required", http.StatusBadRequest)
			return
		}

		store, err := modelstore.Open(*dbPath)
		if err != nil {
			httpError(w, "failed to open model store", http.StatusInternalServerError)
			return
		}
		defer store.Close()
		store.EnableAuthProfileSync("")

		if err := store.SetCredentialEnabled(req.ID, req.Enabled); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		jsonResponse(w, map[string]any{"ok": true, "id": req.ID, "enabled": req.Enabled})
	})

	mux.HandleFunc("/api/credentials/sync", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		store, err := modelstore.Open(*dbPath)
		if err != nil {
			httpError(w, "failed to open model store", http.StatusInternalServerError)
			return
		}
		defer store.Close()
		store.RegisterDefaultOAuthProviders()
		store.EnableAuthProfileSync("")

		updated, err := store.SyncFromAuthProfiles("")
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Also sync back to auth-profiles to keep OpenClaw fresh
		store.SyncToAuthProfiles("")

		jsonResponse(w, map[string]any{"ok": true, "updated": updated})
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		jsonResponse(w, map[string]any{"status": "ok"})
	})

	// NATS handlers for usage queries
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	natsClient, natsErr := bus.Connect(bus.Options{URL: natsURL, Name: "model-store"})
	if natsErr != nil {
		log.Printf("warning: NATS unavailable: %v", natsErr)
	} else {
		defer natsClient.Close()

		natsClient.Reply("usage.query", func(data []byte) (any, error) {
			var req messages.UsageQuery
			if err := json.Unmarshal(data, &req); err != nil {
				return messages.APIResponse{OK: false, Error: err.Error()}, nil
			}

			s, err := openStore()
			if err != nil {
				return messages.APIResponse{OK: false, Error: err.Error()}, nil
			}
			defer s.Close()

			queryPeriod := func(days int) []messages.UsageEntry {
				// Query each day in range
				var entries []messages.UsageEntry
				for d := 0; d < days; d++ {
					date := time.Now().AddDate(0, 0, -d).Format("2006-01-02")
					records, err := s.Usage(req.Agent, date)
					if err != nil {
						continue
					}
					for _, r := range records {
						entries = append(entries, messages.UsageEntry{
							Date:         r.Date,
							Agent:        r.Agent,
							Model:        r.Model,
							Provider:     r.Provider,
							InputTokens:  r.InputTokens,
							OutputTokens: r.OutputTokens,
							Requests:     r.Requests,
							CostUSD:      r.CostUSD,
						})
					}
				}
				if entries == nil {
					entries = []messages.UsageEntry{}
				}
				return entries
			}

			days := req.Days
			if days <= 0 {
				// Return day/week/month
				resp := messages.UsageResponse{
					Day:   queryPeriod(1),
					Week:  queryPeriod(7),
					Month: queryPeriod(30),
				}
				return messages.APIResponse{OK: true, Data: resp}, nil
			}

			entries := queryPeriod(days)
			return messages.APIResponse{OK: true, Data: entries}, nil
		})
		log.Printf("usage.query NATS handler registered")
	}

	server := &http.Server{Addr: *addr, Handler: mux}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down")
		server.Close()
	}()

	log.Printf("listening on %s", *addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func maskKey(key string) string {
	if len(key) <= 16 {
		return strings.Repeat("*", len(key))
	}
	return key[:8] + "..." + key[len(key)-4:]
}

func findBinary(name string) string {
	// Check ~/bin first, then PATH.
	home, _ := os.UserHomeDir()
	candidate := home + "/bin/" + name
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}
