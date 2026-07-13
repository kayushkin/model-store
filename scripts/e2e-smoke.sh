#!/usr/bin/env bash
# Boot-and-answer smoke test for model-store.
#
# READ THIS FIRST — model-store is NOT an HTTP smoke, and that is the finding.
#
# The other store smokes boot a server and drive routes. This one cannot, because
# THE COMMITTED TREE CONTAINS NO HTTP SERVER. Commit 3894313 ("Remove legacy HTTP
# server and consolidate into ms CLI", 2026-04-12) deleted cmd/model-store/main.go
# along with credentials.go, oauth.go and usage.go. What remains under cmd/ is
# `ms`: a Cobra CLI with no serve mode and no --addr flag.
#
# The live :8155 API is still served — by a STALE binary built ~2026-04-06, before
# that deletion, which nothing has redeployed since. It cannot be rebuilt from any
# commit in this history (the deleted server also imported bus/messages, which is
# no longer in go.mod). A copy is preserved at
# ~/.local/share/model-store-binary-backup/.
#
# So this smoke proves what this tree CAN produce: a working `ms` CLI. It boots
# the binary, seeds a throwaway store, and asserts the CLI answers with real
# parsed content — the same contract as the HTTP smokes, one layer down. Deciding
# what model-store's serve mode should be (rewrite it in cmd/ms? resurrect
# cmd/model-store? retire the HTTP API?) is noteboard todo 61fe726c; when that
# lands, this smoke should grow an HTTP phase and the `--addr` pin below will
# fail, which is exactly the reminder whoever fixes it should get.
#
# HERMETICITY — the ONLY lever is $HOME.
# model-store has NO env var and NO flag for the DB path: every ms.Open("") call
# resolves DefaultPath() = $HOME/.config/model-store/store.db, and Open() runs
# MkdirAll + migrate() (including unconditional ALTER TABLEs). So a run with the
# ambient HOME does not merely READ the live store, it MUTATES it. This script
# redirects HOME and then asserts, at the end, that the store really landed in the
# sandbox — because if that ever stops being true, the smoke has been writing to
# the live store all along.
#
# `ms sync` is NEVER run: it makes real outbound calls to the Anthropic, OpenAI
# and Google model APIs.
#
# Exits 0 on success, non-zero on the first failing assertion.
#
# Tunables:
#   E2E_KEEP — set to "1" to leave $TMP_DIR around after the run

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# model-store uses modernc.org/sqlite (pure Go) — no cgo, no C compiler needed.
for bin in go grep; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "ERROR: required tool '$bin' not found on PATH" >&2
    exit 2
  fi
done

TMP_DIR="$(mktemp -d -t model-store-e2e.XXXXXX)"
BIN_DIR="$TMP_DIR/bin"
FAKE_HOME="$TMP_DIR/home"
OUT="$TMP_DIR/out.txt"
mkdir -p "$BIN_DIR" "$FAKE_HOME"

# The sandboxed store. Nothing in this run may touch anything outside it.
SANDBOX_DB="$FAKE_HOME/.config/model-store/store.db"
LIVE_DB="$HOME/.config/model-store/store.db"

cleanup() {
  if [ "${E2E_KEEP:-}" = "1" ]; then
    echo "[e2e] keeping $TMP_DIR"
  else
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

step() { printf '\n==> %s\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  if [ -s "$OUT" ]; then
    echo "----- last command output -----" >&2
    cat "$OUT" >&2
  fi
  exit 1
}

# ms SUBCOMMAND... — run the CLI against the SANDBOXED home. Output → $OUT.
# Returns the CLI's exit code; callers that expect a failure must guard with `||`.
ms() {
  HOME="$FAKE_HOME" "$BIN_DIR/ms" "$@" >"$OUT" 2>&1
}

# assert_out PATTERN WHAT — the last command's output must contain PATTERN.
assert_out() {
  grep -qF -- "$1" "$OUT" || {
    echo "----- last command output -----" >&2
    cat "$OUT" >&2
    echo >&2
    fail "$2: expected the output to contain '$1'"
  }
}

# refute_out PATTERN WHAT — the last command's output must NOT contain PATTERN.
refute_out() {
  if grep -qF -- "$1" "$OUT"; then
    echo "----- last command output -----" >&2
    cat "$OUT" >&2
    echo >&2
    fail "$2: the output must NOT contain '$1'"
  fi
}

# ============================================================================
step "capture the live store's fingerprint, so we can prove we never touched it"
# If the live store exists, remember its mtime+size. The final assertion compares.
LIVE_BEFORE=""
if [ -f "$LIVE_DB" ]; then
  LIVE_BEFORE="$(stat -c '%Y:%s' "$LIVE_DB")"
  echo "    live store: $LIVE_DB ($LIVE_BEFORE)"
else
  echo "    live store: none on this host"
fi

# ============================================================================
step "build cmd/ms from $REPO_DIR"
cd "$REPO_DIR"
go build -o "$BIN_DIR/ms" ./cmd/ms
echo "    binary: $(ls -lh "$BIN_DIR/ms" | awk '{print $5}')"

step "the binary runs at all — --help exits 0 and names its subcommands"
ms --help || fail "ms --help exited non-zero"
assert_out "Available Commands:" "ms --help"
for sub in providers models resolve seed sync enable disable; do
  assert_out "$sub" "ms --help lists the '$sub' subcommand"
done

# ============================================================================
step "DRIFT PIN — the binary the .service runs cannot serve HTTP"
# model-store.service runs `~/bin/model-store --addr :8155`, and deploy.sh builds
# ./cmd/ms into that path. This asserts what that combination actually does: the
# CLI rejects --addr and exits non-zero, which under Restart=always is a permanent
# crash loop. deploy.sh is disarmed for exactly this reason.
#
# WHEN A SERVE MODE IS ADDED (todo 61fe726c), THIS ASSERTION SHOULD FAIL. That is
# the point — it will send whoever adds it back to deploy.sh to re-arm it.
if ms --addr :8155; then
  fail "ms accepted --addr — a serve mode now exists? Then 61fe726c is resolved: teach this smoke to boot it, and RE-ARM deploy.sh (it is currently disarmed)."
fi
assert_out "unknown flag: --addr" "ms --addr is rejected"
echo "    confirmed: 'ms --addr :8155' exits non-zero with 'unknown flag'"

# ============================================================================
step "ms seed — creates the store and populates it (no network)"
ms seed || fail "ms seed exited non-zero"
assert_out "Seeded providers and models." "ms seed"
[ -f "$SANDBOX_DB" ] || fail "ms seed did not create a store at $SANDBOX_DB"
echo "    store: $SANDBOX_DB ($(stat -c %s "$SANDBOX_DB") bytes)"

step "ms providers — reads the seeded providers back out of sqlite"
ms providers || fail "ms providers exited non-zero"
for p in anthropic openai google ollama openrouter; do
  assert_out "$p" "ms providers lists '$p'"
done

step "ms models — reads the seeded models back, with real pricing"
ms models || fail "ms models exited non-zero"
assert_out "Anthropic:"        "ms models groups by provider"
assert_out "claude-opus-4-6"   "ms models lists a seeded model"
assert_out "per MTok"          "ms models renders pricing"

step "ms models <provider> — the provider filter narrows the list"
ms models anthropic || fail "ms models anthropic exited non-zero"
assert_out "claude-opus-4-6" "ms models anthropic lists an anthropic model"

# ============================================================================
step "ms resolve <id> — resolves a model to its full record"
ms resolve claude-sonnet-4-6 || fail "ms resolve claude-sonnet-4-6 exited non-zero"
assert_out "Model:"     "ms resolve renders the model line"
assert_out "Provider: anthropic" "ms resolve renders the provider"
assert_out "Enabled:  true"      "a seeded model is enabled"

step "ms resolve <alias> — an ALIAS resolves to the underlying model id"
# Real logic, not a lookup: `claude-opus` is an alias, not a model id.
ms resolve claude-opus || fail "ms resolve claude-opus exited non-zero"
assert_out "claude-opus-4-6" "the 'claude-opus' alias resolved to claude-opus-4-6"

step "ms resolve <unknown> — fails loudly, does not print an empty record"
if ms resolve definitely-not-a-real-model-xyz; then
  fail "ms resolve on an unknown model exited 0 — it must fail loudly"
fi
assert_out "model not found" "ms resolve on an unknown model names the problem"

# ============================================================================
step "ms disable / ms enable — the write path, and it really persists"
ms disable claude-sonnet-4-6 || fail "ms disable exited non-zero"
assert_out "Disabled claude-sonnet-4-6" "ms disable"
# Read it back through a SEPARATE process — proves it hit sqlite, not just memory.
ms resolve claude-sonnet-4-6 || fail "ms resolve after disable exited non-zero"
assert_out "Enabled:  false" "the disable persisted to the store"

ms enable claude-sonnet-4-6 || fail "ms enable exited non-zero"
assert_out "Enabled claude-sonnet-4-6" "ms enable"
ms resolve claude-sonnet-4-6 || fail "ms resolve after enable exited non-zero"
assert_out "Enabled:  true" "the enable persisted to the store"

step "ms seed is idempotent — a second seed does not explode"
ms seed || fail "a second ms seed exited non-zero"
ms resolve claude-sonnet-4-6 || fail "ms resolve after re-seed exited non-zero"
assert_out "Enabled:  true" "the store still answers after a re-seed"

# ============================================================================
# Hermeticity. HOME is the only lever there is, so this is not a formality.
# ============================================================================
step "hermeticity: the store landed in the sandbox, and the LIVE store is untouched"
[ -f "$SANDBOX_DB" ] || fail "no store under the sandboxed HOME — is DefaultPath() still \$HOME-relative?"

if [ -n "$LIVE_BEFORE" ]; then
  LIVE_AFTER="$(stat -c '%Y:%s' "$LIVE_DB")"
  [ "$LIVE_BEFORE" = "$LIVE_AFTER" ] \
    || fail "THE LIVE MODEL STORE WAS MODIFIED ($LIVE_BEFORE -> $LIVE_AFTER). ms.Open() must be ignoring \$HOME."
  echo "    live store unchanged: $LIVE_AFTER"
else
  echo "    no live store to protect on this host"
fi
echo "    only $SANDBOX_DB was written"

step "SUCCESS — model-store's CLI boots and answers from this tree"
echo "    (there is still no HTTP server to smoke — see the header, and todo 61fe726c)"
