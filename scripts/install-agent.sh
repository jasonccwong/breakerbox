#!/bin/sh
# BreakerBox agent installer (macOS + Linux).
#
#   curl -fsSL https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-agent.sh \
#     | sh -s -- --hub https://hub.example.com --token <ENROLL_TOKEN>
#
# Flags:
#   --hub URL       hub base URL (required unless already enrolled)
#   --token TOKEN   one-time enrollment token (required unless already enrolled)
#   --version VER   release version like 0.1.0-alpha (default: latest)
#   --no-service    install the binary and enroll, but skip service setup
#
# Env overrides (mainly for CI/e2e):
#   BREAKERBOX_LOCAL_BINARY  path to a prebuilt agent binary: skips download
#   BREAKERBOX_BIN_DIR       install dir (default /usr/local/bin or ~/.local/bin)
set -eu

REPO="jasonccwong/breakerbox"
HUB="" TOKEN="" VERSION="" NO_SERVICE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --hub) HUB="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --no-service) NO_SERVICE=1; shift ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "unsupported OS: $OS (Windows: use install-agent.ps1)" >&2; exit 1 ;;
esac
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

# Pick an install dir we can write to.
if [ -n "${BREAKERBOX_BIN_DIR:-}" ]; then
  BIN_DIR="$BREAKERBOX_BIN_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null || [ "$(id -u)" = 0 ]; then
  BIN_DIR=/usr/local/bin
else
  BIN_DIR="$HOME/.local/bin"
fi
mkdir -p "$BIN_DIR"
BIN="$BIN_DIR/breakerbox-agent"

if [ -n "${BREAKERBOX_LOCAL_BINARY:-}" ]; then
  echo "» installing local binary $BREAKERBOX_LOCAL_BINARY"
  cp "$BREAKERBOX_LOCAL_BINARY" "$BIN"
else
  if [ -z "$VERSION" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
      | grep '"tag_name"' | head -1 | sed 's/.*"v\{0,1\}\([^"]*\)".*/\1/')
    [ -n "$VERSION" ] || { echo "could not resolve latest release" >&2; exit 1; }
  fi
  URL="https://github.com/$REPO/releases/download/v$VERSION/breakerbox-agent_${VERSION}_${OS}_${ARCH}.tar.gz"
  echo "» downloading $URL"
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  curl -fsSL "$URL" | tar -xz -C "$TMP"
  install -m 0755 "$TMP/breakerbox-agent" "$BIN"
fi
chmod +x "$BIN"
echo "» installed $("$BIN" version 2>/dev/null || echo breakerbox-agent) at $BIN"

# Enroll when hub+token given (idempotent: re-enrolling replaces the binding).
if [ -n "$HUB" ] && [ -n "$TOKEN" ]; then
  echo "» enrolling with hub $HUB"
  "$BIN" enroll --hub "$HUB" --token "$TOKEN"
elif [ -n "$HUB$TOKEN" ]; then
  echo "!! need BOTH --hub and --token to enroll" >&2; exit 2
fi

[ "$NO_SERVICE" = 1 ] && { echo "» service setup skipped (--no-service)"; exit 0; }

case "$OS" in
  darwin)
    PLIST="$HOME/Library/LaunchAgents/com.breakerbox.agent.plist"
    mkdir -p "$(dirname "$PLIST")"
    cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.breakerbox.agent</string>
  <key>ProgramArguments</key><array>
    <string>$BIN</string><string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/breakerbox-agent.log</string>
  <key>StandardErrorPath</key><string>/tmp/breakerbox-agent.log</string>
</dict></plist>
EOF
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl load "$PLIST"
    echo "» launchd service loaded (logs: /tmp/breakerbox-agent.log)"
    ;;
  linux)
    if [ "$(id -u)" = 0 ] && command -v systemctl >/dev/null 2>&1; then
      id breakerbox >/dev/null 2>&1 || useradd --system --home /var/lib/breakerbox-agent --shell /usr/sbin/nologin breakerbox
      mkdir -p /var/lib/breakerbox-agent && chown breakerbox: /var/lib/breakerbox-agent
      # Enrollment ran as root above: hand the state over to the service user.
      [ -d /root/.local/share/breakerbox-agent ] && cp -a /root/.local/share/breakerbox-agent/. /var/lib/breakerbox-agent/ && chown -R breakerbox: /var/lib/breakerbox-agent
      cat > /etc/systemd/system/breakerbox-agent.service <<EOF
[Unit]
Description=BreakerBox agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=breakerbox
Environment=BREAKERBOX_STATE_DIR=/var/lib/breakerbox-agent
ExecStart=$BIN run
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
      systemctl daemon-reload
      systemctl enable --now breakerbox-agent
      echo "» systemd service enabled (journalctl -u breakerbox-agent -f)"
    elif command -v systemctl >/dev/null 2>&1; then
      mkdir -p "$HOME/.config/systemd/user"
      cat > "$HOME/.config/systemd/user/breakerbox-agent.service" <<EOF
[Unit]
Description=BreakerBox agent

[Service]
ExecStart=$BIN run
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF
      systemctl --user daemon-reload
      systemctl --user enable --now breakerbox-agent
      echo "» user systemd service enabled (systemctl --user status breakerbox-agent)"
      echo "  tip: 'sudo loginctl enable-linger $USER' keeps it running after logout"
    else
      echo "!! no systemd found; start manually with: $BIN run" >&2
    fi
    ;;
esac
echo "✔ done"
