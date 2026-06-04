#!/usr/bin/env sh
set -eu

# Removes the lookout service and binary. Keeps config and the service user by
# default so a reinstall preserves your settings.
#
# Usage (as root):
#   sudo sh uninstall.sh           # remove service + binary, keep config
#   sudo sh uninstall.sh --purge   # also remove /etc/lookout and the user

BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/lookout"
UNIT_FILE="/etc/systemd/system/lookout.service"
SERVICE_USER="lookout"

PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

fail() { echo "error: $*" >&2; exit 1; }
[ "$(id -u)" -eq 0 ] || fail "please run as root (e.g. with sudo)"

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop lookout 2>/dev/null || true
  systemctl disable lookout 2>/dev/null || true
fi

rm -f "$UNIT_FILE"
command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload || true
rm -f "${BIN_DIR}/lookout"
echo "Removed service and binary."

if [ "$PURGE" -eq 1 ]; then
  rm -rf "$CONF_DIR"
  id "$SERVICE_USER" >/dev/null 2>&1 && userdel "$SERVICE_USER" 2>/dev/null || true
  echo "Purged config (${CONF_DIR}) and user '${SERVICE_USER}'."
else
  echo "Kept config (${CONF_DIR}) and user '${SERVICE_USER}'. Use --purge to remove them."
fi
