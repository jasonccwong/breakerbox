#!/bin/sh
# BreakerBox hub installer (macOS + Linux).
#
#   curl -fsSL https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-hub.sh | sh
#
# Flags:
#   --version VER   release version like 0.1.0-alpha (default: latest)
#   --port PORT     listen port (default 8090)
#   --no-service    install the binary only, skip service setup
#
# Env overrides:
#   BREAKERBOX_LOCAL_BINARY  path to a prebuilt hub binary: skips download
#   BREAKERBOX_BIN_DIR       install dir (default /usr/local/bin or ~/.local/bin)
set -eu

REPO="jasonccwong/breakerbox"
VERSION="" PORT=8090 NO_SERVICE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --no-service) NO_SERVICE=1; shift ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in darwin|linux) ;; *) echo "unsupported OS: $OS" >&2; exit 1 ;; esac
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

if [ -n "${BREAKERBOX_BIN_DIR:-}" ]; then
  BIN_DIR="$BREAKERBOX_BIN_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null || [ "$(id -u)" = 0 ]; then
  BIN_DIR=/usr/local/bin
else
  BIN_DIR="$HOME/.local/bin"
fi
mkdir -p "$BIN_DIR"
BIN="$BIN_DIR/breakerbox-hub"

if [ -n "${BREAKERBOX_LOCAL_BINARY:-}" ]; then
  echo "» installing local binary $BREAKERBOX_LOCAL_BINARY"
  cp "$BREAKERBOX_LOCAL_BINARY" "$BIN"
else
  if [ -z "$VERSION" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
      | grep '"tag_name"' | head -1 | sed 's/.*"v\{0,1\}\([^"]*\)".*/\1/')
    [ -n "$VERSION" ] || { echo "could not resolve latest release" >&2; exit 1; }
  fi
  URL="https://github.com/$REPO/releases/download/v$VERSION/breakerbox-hub_${VERSION}_${OS}_${ARCH}.tar.gz"
  echo "» downloading $URL"
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  curl -fsSL "$URL" | tar -xz -C "$TMP"
  install -m 0755 "$TMP/breakerbox-hub" "$BIN"
fi
chmod +x "$BIN"

# Data dir keeps the SQLite database.
if [ "$(id -u)" = 0 ]; then DATA_DIR=/var/lib/breakerbox-hub
else DATA_DIR="$HOME/.local/share/breakerbox-hub"; fi
mkdir -p "$DATA_DIR"

if [ "$NO_SERVICE" = 1 ]; then
  echo "» service setup skipped; run manually:"
  echo "  $BIN serve --http 0.0.0.0:$PORT --dir $DATA_DIR"
  exit 0
fi

case "$OS" in
  darwin)
    PLIST="$HOME/Library/LaunchAgents/com.breakerbox.hub.plist"
    mkdir -p "$(dirname "$PLIST")"
    cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.breakerbox.hub</string>
  <key>ProgramArguments</key><array>
    <string>$BIN</string><string>serve</string>
    <string>--http</string><string>0.0.0.0:$PORT</string>
    <string>--dir</string><string>$DATA_DIR</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/breakerbox-hub.log</string>
  <key>StandardErrorPath</key><string>/tmp/breakerbox-hub.log</string>
</dict></plist>
EOF
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl load "$PLIST"
    echo "» launchd service loaded (logs: /tmp/breakerbox-hub.log)"
    ;;
  linux)
    if [ "$(id -u)" = 0 ] && command -v systemctl >/dev/null 2>&1; then
      id breakerbox-hub >/dev/null 2>&1 || useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin breakerbox-hub
      chown -R breakerbox-hub: "$DATA_DIR"
      cat > /etc/systemd/system/breakerbox-hub.service <<EOF
[Unit]
Description=BreakerBox hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=breakerbox-hub
ExecStart=$BIN serve --http 0.0.0.0:$PORT --dir $DATA_DIR
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
      systemctl daemon-reload
      systemctl enable --now breakerbox-hub
      echo "» systemd service enabled (journalctl -u breakerbox-hub -f)"
    elif command -v systemctl >/dev/null 2>&1; then
      mkdir -p "$HOME/.config/systemd/user"
      cat > "$HOME/.config/systemd/user/breakerbox-hub.service" <<EOF
[Unit]
Description=BreakerBox hub

[Service]
ExecStart=$BIN serve --http 0.0.0.0:$PORT --dir $DATA_DIR
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF
      systemctl --user daemon-reload
      systemctl --user enable --now breakerbox-hub
      echo "» user systemd service enabled"
    else
      echo "!! no systemd found; start manually with: $BIN serve --http 0.0.0.0:$PORT --dir $DATA_DIR" >&2
    fi
    ;;
esac
echo "✔ hub running — open http://localhost:$PORT to finish setup"
