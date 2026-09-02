#!/usr/bin/env bash
set -euo pipefail

# Builds the ms CLI (which now carries the `serve` subcommand), installs it as
# both ~/bin/ms and ~/bin/model-store, syncs the systemd unit, restarts the
# service, and smoke-checks the HTTP surface.
#
# History: this script was DISARMED 2026-07-13 because commit 3894313 had
# deleted the HTTP server while the unit still ran `model-store --addr :8155`
# — deploying the CLI over the irreplaceable pre-deletion binary would have
# crash-looped the service with no way back. The serve subcommand added to
# cmd/ms restores a buildable server, so the script is armed again. The last
# pre-deletion binary is preserved at ~/.local/share/model-store-binary-backup/.

cd "$(dirname "$0")"

echo "==> go test ./..."
go test ./...

echo "==> building cmd/ms"
go build -o /tmp/model-store-deploy-build ./cmd/ms

echo "==> installing ~/bin/ms and ~/bin/model-store"
install -m 0755 /tmp/model-store-deploy-build "$HOME/bin/ms"
install -m 0755 /tmp/model-store-deploy-build "$HOME/bin/model-store"
rm /tmp/model-store-deploy-build

echo "==> syncing systemd unit"
install -m 0644 model-store.service "$HOME/.config/systemd/user/model-store.service"
systemctl --user daemon-reload

echo "==> restarting model-store.service"
systemctl --user restart model-store.service

echo "==> smoke check"
sleep 1
systemctl --user is-active model-store.service
curl -sfS http://localhost:8155/api/health >/dev/null
# The rewrite's point: /api/models must carry short_name, which the stale
# 2026-04-06 binary did not serve.
curl -sfS http://localhost:8155/api/models | grep -q '"short_name"'
echo "==> deployed: :8155 serving /api/models with short_name"
