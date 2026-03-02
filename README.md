# model-store

Centralized model registry, auth, and usage tracking for AI agents.

One place for all providers, keys, and token usage — shared across inber, sí, OpenClaw, and any other consumer.

## Features

- **Provider registry** — Anthropic, OpenAI, Google, local models
- **Key management** — API keys, OAuth tokens, auto-refresh
- **Model catalog** — available models per provider with aliases
- **Usage tracking** — tokens in/out per agent per model per day
- **Go library** — import directly, no network service needed
- **CLI** — `ms list`, `ms usage`, `ms keys`, `ms add`

## Storage

SQLite at `~/.config/model-store/store.db`. No server needed.

## Usage

```go
import ms "github.com/kayushkin/model-store"

store, _ := ms.Open("")  // default path

// Resolve credentials for a provider
creds, _ := store.Resolve("anthropic")
// creds.APIKey or creds.Token

// List available models
models, _ := store.Models("anthropic")

// Track usage
store.TrackUsage("inber", "claude-sonnet-4-5", 45000, 3000)

// Query usage
usage, _ := store.Usage("inber", "2026-03-02")
```

## CLI

```bash
ms providers                     # list configured providers
ms models anthropic              # list models for provider
ms keys add anthropic sk-...     # add API key
ms keys add anthropic --oauth    # add OAuth token (interactive)
ms usage --today                 # today's usage across all agents
ms usage --agent inber --week    # inber's usage this week
```
