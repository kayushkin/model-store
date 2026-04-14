# model-store

Centralized model registry for AI agents. Tracks providers, models, aliases, pricing, and health — shared across the [llm-bridge](https://github.com/kayushkin/llm-bridge) ecosystem and any other consumer.

One SQLite database at `~/.config/model-store/store.db`. No server needed — import the Go library directly or use the CLI.

## Features

- **Provider registry** — Anthropic, OpenAI, Google, Ollama, OpenRouter
- **Model catalog** — models per provider with aliases, context window, and pricing
- **Alias resolution** — resolve short names like `sonnet` or `haiku` to full model IDs
- **Priority & failover** — enabled/disabled state and priority ordering for failover chains
- **Live sync** — fetch current model lists from Anthropic, OpenAI, and Google APIs
- **Health tracking** — record success/error metrics per model with rolling averages
- **Go library** — import directly, no network service needed
- **CLI** — `ms` binary for interactive use

## CLI

```bash
ms providers                  # list configured providers
ms models                     # list all models with pricing and aliases
ms models anthropic           # list models for a specific provider
ms resolve sonnet             # resolve alias to full model details
ms seed                       # populate default providers and models
ms sync                       # fetch live models from all provider APIs
ms sync anthropic google      # sync specific providers
ms enable claude-opus-4-6     # enable a model
ms disable gpt-4o             # disable a model
```

### Sync

`ms sync` fetches the current model list from each provider's API and upserts into the store. Existing models are updated (name, context window) but user-set fields (enabled, priority, cost, aliases) are preserved. New models are added with priority 100 and estimated costs.

Requires API keys via environment variables (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY`) or [aiauth](https://github.com/kayushkin/aiauth) profiles.

## Go library

```go
import ms "github.com/kayushkin/model-store"

store, _ := ms.Open("")  // default path
defer store.Close()

// List providers and models
providers, _ := store.Providers()
models, _ := store.Models("anthropic")

// Resolve by ID or alias
model, _ := store.ResolveModel("sonnet")
// model.ID = "claude-sonnet-4-5-20250929", model.Provider = "anthropic"

// Look up provider for a model
provider, _ := store.ProviderFor("claude-opus-4-6")

// Failover chain (enabled models, sorted by priority)
chain, _ := store.FailoverChain()

// Enable/disable
store.SetEnabled("gpt-4o", false)

// Health tracking
store.RecordSuccess("claude-sonnet-4-5-20250929", 1200) // 1200ms
store.RecordError("gpt-4o", "rate limited")

health := store.GetHealth("claude-sonnet-4-5-20250929")
health.IsHealthy(5 * time.Minute)  // true if successful within window

// All models with health data (for dashboards)
statuses, _ := store.AllModelsWithStatus()

// Seed defaults and sync from APIs
store.Seed()
result, _ := store.SyncProvider("anthropic", apiKey)
```

## Schema

```sql
CREATE TABLE providers (
    id   TEXT PRIMARY KEY,     -- "anthropic", "openai", "google", ...
    name TEXT NOT NULL          -- display name
);

CREATE TABLE models (
    id          TEXT PRIMARY KEY,   -- "claude-sonnet-4-5-20250929"
    provider    TEXT NOT NULL,      -- references providers(id)
    name        TEXT NOT NULL,      -- display name
    max_tokens  INTEGER,           -- context window
    input_cost  REAL,              -- $ per million tokens
    output_cost REAL,              -- $ per million tokens
    enabled     INTEGER,           -- 1 = available, 0 = disabled
    priority    INTEGER            -- failover order (lower = preferred)
);

CREATE TABLE model_aliases (
    alias    TEXT PRIMARY KEY,     -- "sonnet", "haiku", "4o"
    model_id TEXT NOT NULL         -- references models(id)
);

CREATE TABLE model_health (
    model             TEXT PRIMARY KEY,
    last_success_at   INTEGER,     -- unix timestamp
    last_success_ms   INTEGER,     -- response duration
    avg_response_ms   INTEGER,     -- exponential moving average
    last_error_at     INTEGER,     -- unix timestamp
    last_error        TEXT,
    consecutive_errors INTEGER
);
```

## Part of the llm-bridge ecosystem

model-store is one of several optional stores used by [llm-bridge-server](https://github.com/kayushkin/llm-bridge-server). When loaded, it powers the `/models` endpoint and provides model resolution for session configuration. See the [llm-bridge README](https://github.com/kayushkin/llm-bridge) for the full picture.
