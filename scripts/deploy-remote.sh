#!/usr/bin/env bash
# Build the model-registry plugin on a remote CPA host and restart the service.
#
# The plugin is -buildmode=c-shared with CGO enabled, so it has to be built where
# it runs; a macOS host cannot produce a linux/amd64 .so without a cross toolchain.
#
# Usage: ./scripts/deploy-remote.sh <ssh-host> [plugin-dir] [service]
#   ./scripts/deploy-remote.sh cpa /opt/cpa/plugins cpa
set -euo pipefail

SSH_HOST="${1:?usage: deploy-remote.sh <ssh-host> [plugin-dir] [service]}"
PLUGIN_DIR="${2:-/opt/cpa/plugins}"
SERVICE="${3:-cpa}"
SRC_DIR=/opt/src/cpa-plugin-model-registry

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCHIVE="$(mktemp -t model-registry.XXXXXX).tar.gz"
trap 'rm -f "$ARCHIVE"' EXIT

PLUGIN_VERSION="${PLUGIN_VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo dev)}"
echo "==> version $PLUGIN_VERSION"

echo "==> packaging HEAD from $REPO_ROOT"
git -C "$REPO_ROOT" archive --format=tar.gz --prefix=cpa-plugin-model-registry/ -o "$ARCHIVE" HEAD

echo "==> uploading to $SSH_HOST"
scp -q "$ARCHIVE" "$SSH_HOST:/tmp/model-registry-src.tar.gz"

echo "==> building on $SSH_HOST"
ssh "$SSH_HOST" "set -euo pipefail
export PATH=/usr/local/go/bin:\$PATH
export HOME=\${HOME:-/root}
command -v go >/dev/null || { echo 'go is not installed at /usr/local/go/bin/go' >&2; exit 1; }
sudo mkdir -p $(dirname $SRC_DIR)
sudo rm -rf $SRC_DIR
sudo tar -xzf /tmp/model-registry-src.tar.gz -C $(dirname $SRC_DIR)
cd $SRC_DIR
sudo env PATH=/usr/local/go/bin:\$PATH HOME=\$HOME go test -count=1 ./...
sudo env PATH=/usr/local/go/bin:\$PATH HOME=\$HOME CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -ldflags '-s -w -X main.version=$PLUGIN_VERSION' -o /tmp/model-registry.so .
file /tmp/model-registry.so"

echo "==> installing and restarting $SERVICE"
ssh "$SSH_HOST" "set -euo pipefail
sudo install -m 755 /tmp/model-registry.so $PLUGIN_DIR/model-registry.so
sudo rm -rf $SRC_DIR /tmp/model-registry.so /tmp/model-registry-src.tar.gz
sudo systemctl restart $SERVICE
sleep 3
sudo systemctl is-active $SERVICE
sudo journalctl -u $SERVICE --since '1 min ago' --no-pager | grep -iE 'model-registry|plugin registered' || true"

echo "==> done"
