#!/bin/sh
# Installs the DoxaCast agent on Raspberry Pi OS Lite (Bookworm or newer).
#
#   sudo ./install.sh ./doxacast-agent-linux-arm64 http://server:8080 <enrollment-token>
#
# Safe to re-run: it upgrades the binary and leaves existing config alone.
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root (sudo)" >&2
  exit 1
fi
if [ $# -lt 1 ]; then
  echo "usage: $0 <agent-binary> [server-url] [enrollment-token]" >&2
  exit 1
fi

BIN=$1
SERVER=${2:-}
TOKEN=${3:-}
HERE=$(cd "$(dirname "$0")" && pwd)

echo "==> installing packages (mpv, ffmpeg)"
apt-get update -q
apt-get install -y -q mpv ffmpeg

echo "==> creating doxacast user"
if ! id doxacast >/dev/null 2>&1; then
  useradd --system --home /var/lib/doxacast --shell /usr/sbin/nologin doxacast
fi
for g in video render audio input; do
  getent group "$g" >/dev/null && usermod -aG "$g" doxacast
done

echo "==> installing binary"
install -m 0755 "$BIN" /usr/local/bin/doxacast-agent

echo "==> config"
install -d -m 0755 /etc/doxacast
if [ ! -f /etc/doxacast/agent.json ]; then
  if [ -z "$SERVER" ] || [ -z "$TOKEN" ]; then
    echo "no existing config: pass server-url and enrollment-token" >&2
    exit 1
  fi
  sed -e "s#http://doxacast.local:8080#${SERVER}#" \
      -e "s#paste DOXACAST_ENROLLMENT_TOKEN here#${TOKEN}#" \
      "$HERE/agent.example.json" > /etc/doxacast/agent.json
  chown root:doxacast /etc/doxacast/agent.json
  chmod 0640 /etc/doxacast/agent.json
fi

echo "==> hardware watchdog (reboots the Pi if the system hangs)"
install -d /etc/systemd/system.conf.d
cat > /etc/systemd/system.conf.d/doxacast-watchdog.conf <<'EOF'
[Manager]
RuntimeWatchdogSec=15
RebootWatchdogSec=2min
EOF

echo "==> service"
install -m 0644 "$HERE/doxacast-agent.service" /etc/systemd/system/doxacast-agent.service
systemctl daemon-reload
systemctl enable doxacast-agent
systemctl restart doxacast-agent

echo
echo "Done. Watch it with: journalctl -fu doxacast-agent"
echo "The screen will appear in the admin console once it enrolls."
echo "Composite output? See docs/hardware.md (needs a config.txt change + reboot)."
