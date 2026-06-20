#!/usr/bin/env sh
set -eu

# Lookout installer. Downloads the release binary matching this server's
# architecture, verifies its checksum, installs it as a systemd service running
# under a dedicated unprivileged user, and writes a YAML config template.
#
# Usage (as root):
#   curl -fsSL https://raw.githubusercontent.com/AmoabaKelvin/lookout/main/install.sh | sudo sh
#   VERSION=v1.0.0 sudo sh install.sh    # pin a specific version
#
# Re-running upgrades the binary in place and never overwrites an existing config.

REPO="AmoabaKelvin/lookout"
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/lookout"
CONF_FILE="${CONF_DIR}/config.yaml"
UNIT_FILE="/etc/systemd/system/lookout.service"
SERVICE_USER="lookout"

fail() { echo "error: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "please run as root (e.g. with sudo)"
command -v systemctl >/dev/null 2>&1 || fail "systemd is required (systemctl not found)"
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

# --- detect architecture ---
case "$(uname -m)" in
  x86_64 | amd64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac
ASSET="lookout-linux-${ARCH}"

# --- resolve version (latest release unless VERSION is set) ---
if [ -z "${VERSION:-}" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d '"' -f4)
  [ -n "$VERSION" ] || fail "could not determine latest version; set VERSION=vX.Y.Z"
fi
echo "Installing lookout ${VERSION} (${ARCH})"

BASE="https://github.com/${REPO}/releases/download/${VERSION}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# --- download + verify checksum ---
echo "Downloading ${ASSET}..."
curl -fsSL "${BASE}/${ASSET}" -o "${TMP}/${ASSET}"
curl -fsSL "${BASE}/checksums.txt" -o "${TMP}/checksums.txt"

echo "Verifying checksum..."
expected=$(awk -v f="$ASSET" '$2 == f {print $1}' "${TMP}/checksums.txt")
[ -n "$expected" ] || fail "no checksum found for ${ASSET}"
actual=$(sha256sum "${TMP}/${ASSET}" | awk '{print $1}')
[ "$expected" = "$actual" ] || fail "checksum mismatch (expected ${expected}, got ${actual})"

# --- install binary ---
install -m 0755 "${TMP}/${ASSET}" "${BIN_DIR}/lookout"
echo "Installed binary -> ${BIN_DIR}/lookout"

# --- create unprivileged service user (no login, no home) ---
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER" 2>/dev/null \
    || useradd --system --no-create-home --shell /sbin/nologin "$SERVICE_USER"
  echo "Created system user '${SERVICE_USER}'"
fi

# --- config (downloaded with the release; never overwrite an existing config) ---
# The template is deploy/config.example.yaml, shipped as a release asset, so the
# config layout has a single source of truth instead of a copy embedded here.
mkdir -p "$CONF_DIR"
if [ -f "$CONF_FILE" ]; then
  echo "Kept existing config -> ${CONF_FILE}"
elif curl -fsSL "${BASE}/config.example.yaml" -o "${TMP}/config.example.yaml"; then
  expected_conf=$(awk '$2 == "config.example.yaml" {print $1}' "${TMP}/checksums.txt")
  [ -n "$expected_conf" ] || fail "no checksum found for config.example.yaml"
  actual_conf=$(sha256sum "${TMP}/config.example.yaml" | awk '{print $1}')
  [ "$expected_conf" = "$actual_conf" ] || fail "config checksum mismatch (expected ${expected_conf}, got ${actual_conf})"
  install -m 600 "${TMP}/config.example.yaml" "$CONF_FILE"
  echo "Created config -> ${CONF_FILE}"
else
  echo "Could not download the config template; lookout will run on built-in defaults."
  echo "Create ${CONF_FILE} later from deploy/config.example.yaml if you want to customize it."
fi
if [ -f "$CONF_FILE" ]; then
  chmod 600 "$CONF_FILE"
  chown "${SERVICE_USER}:${SERVICE_USER}" "$CONF_FILE"
fi

# --- systemd unit ---
cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Lookout monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
ExecStart=${BIN_DIR}/lookout --config ${CONF_FILE}
Restart=always
RestartSec=5
StateDirectory=lookout
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
echo "Installed service -> ${UNIT_FILE}"

systemctl daemon-reload
systemctl enable --now lookout

echo
echo "lookout ${VERSION} is installed and running (alerts go to the journal until a notifier is set)."
echo
echo "Next steps:"
echo "  1. Add a notifier:     sudo nano ${CONF_FILE}"
echo "       uncomment a notifier under 'notifiers:' and paste your webhook URL"
echo "  2. Restart:            sudo systemctl restart lookout"
echo "  3. Watch logs:         journalctl -u lookout -f"
echo
echo "Verify your webhook actually delivers (after step 1):"
echo "  curl -X POST -H 'Content-Type: application/json' -d '{\"text\":\"lookout test\"}' '<your-webhook-url>'"
