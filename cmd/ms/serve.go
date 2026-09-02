package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	ms "github.com/kayushkin/model-store"
	"github.com/spf13/cobra"
)

// newServeCommand restores the HTTP surface that commit 3894313 deleted along
// with the legacy cmd/model-store server. It serves only what today's callers
// use — the model registry — rebuilt on the current library, so the response
// now carries short_name and every column the stale 2026-04-06 binary dropped.
//
// The credential routes are deliberately not reimplemented: auth-store on
// :8303 is the credential vault now, and the library's credential code was
// removed in 76cc62c. They answer 410 Gone naming the replacement, so a
// caller learns where to go instead of getting a silent 404.
func newServeCommand() *cobra.Command {
	var addr string
	var dbPath string

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the model registry over HTTP",
		Long: `Serves the model registry over HTTP for callers that cannot import the Go
library — the model-poll scheduler job and the kayushkin.com proxy routes.

Routes:
  GET  /api/models         all models with health, sorted by provider then priority
  POST /api/models/toggle  {"model": "<id>", "enabled": bool}
  GET  /api/health         liveness probe

The legacy credential routes (/api/credentials, /api/credentials/toggle) and
/api/models/test answer 410 Gone: credentials moved to auth-store on :8303.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			server := &http.Server{Addr: addr, Handler: newServeHandler(store)}
			log.Printf("model-store serving on %s (db %s)", addr, ms.DefaultPath())
			return server.ListenAndServe()
		},
	}

	// :8155 is the canonical model-store port. The pre-deletion server
	// defaulted to :8150, which forge now owns — do not restore that.
	serveCmd.Flags().StringVar(&addr, "addr", ":8155", "listen address")
	serveCmd.Flags().StringVar(&dbPath, "db", "", "store path (default ~/.config/model-store/store.db)")
	return serveCmd
}

func newServeHandler(store *ms.Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		statuses, err := store.AllModelsWithStatus()
		if err != nil {
			httpError(w, fmt.Sprintf("failed to query models: %v", err), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, statuses)
	})

	mux.HandleFunc("/api/models/toggle", func(w http.ResponseWriter, r *http.Request) {
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
		if err := store.SetEnabled(req.Model, req.Enabled); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]any{"ok": true, "model": req.Model, "enabled": req.Enabled})
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"ok": true})
	})

	gone := func(w http.ResponseWriter, r *http.Request) {
		httpError(w, "credentials moved to auth-store on :8303; this route is gone", http.StatusGone)
	}
	mux.HandleFunc("/api/credentials", gone)
	mux.HandleFunc("/api/credentials/toggle", gone)
	mux.HandleFunc("/api/models/test", func(w http.ResponseWriter, r *http.Request) {
		httpError(w, "live model testing was removed with the legacy server; this route is gone", http.StatusGone)
	})

	return mux
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
