#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# DISARMED 2026-07-13 — this script destroyed the service it claimed to deploy.
#
# model-store.service runs:  ~/bin/model-store --addr :8155
# ...but the HTTP server it needs NO LONGER EXISTS IN THIS REPO. Commit 3894313
# ("Remove legacy HTTP server and consolidate into ms CLI", 2026-04-12) deleted
# cmd/model-store/main.go along with credentials.go, oauth.go and usage.go.
# What remains under cmd/ is `ms`, a Cobra CLI with no serve mode and no --addr
# flag.
#
# The live :8155 API is served by a STALE binary built ~2026-04-06, before that
# deletion. It is the only artifact in existence that can serve those routes:
# the deleted server also imported github.com/kayushkin/bus/messages, which is
# no longer in go.mod, so restoring the file alone will not rebuild it.
#
# This script used to:
#   1. `go build -o model-store ./cmd/ms`  -> overwrite the repo-root copy of
#      that irreplaceable binary with the CLI,
#   2. `cp model-store ~/bin/model-store`  -> overwrite the LIVE one too,
#   3. `systemctl --user start model-store.service` -> which then runs the CLI
#      with `--addr :8155`, and Cobra exits 1 on `unknown flag: --addr`.
#      With Restart=always/RestartSec=5, that is a permanent crash loop.
#
# So a single run would have destroyed both copies of the serving binary and
# taken the model API down for good. kayushkin.com's model + credentials UI
# proxies :8155 (MODEL_STORE_URL in /etc/systemd/system/kayushkin.service), so
# it would have started 502ing with no way back.
#
# A copy is preserved at:
#   ~/.local/share/model-store-binary-backup/
#
# Restoring a real deploy path requires deciding what model-store's serve mode
# should be (rewrite it in cmd/ms? resurrect cmd/model-store? retire the HTTP
# API and move its consumers?). That is noteboard todo 61fe726c, and it is a
# design decision, not a mechanical fix — so this script refuses to run rather
# than guess.
#
# To deploy the CLI ONLY (which is all this tree can build), install it under a
# name the service does not use:
#   go build -o ~/bin/ms ./cmd/ms
# ============================================================================

cat >&2 <<'EOF'
ERROR: model-store/deploy.sh is disarmed and will not run.

This repo can no longer build the binary that model-store.service runs. The HTTP
server was deleted in commit 3894313; only the `ms` CLI remains, and it has no
--addr flag. Deploying it would overwrite the live serving binary (:8155) with a
CLI that instantly exits 1, crash-looping the unit and breaking kayushkin.com's
model UI -- irreversibly, because the serving binary cannot be rebuilt from this
tree.

  live binary : ~/bin/model-store  (built ~2026-04-06, pre-deletion)
  backup      : ~/.local/share/model-store-binary-backup/
  decision    : noteboard todo 61fe726c (model-store has no serve mode)

To build the CLI without touching the service:
  go build -o ~/bin/ms ./cmd/ms
EOF
exit 1
