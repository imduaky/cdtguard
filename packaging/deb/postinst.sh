#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
cat <<'EOF'
cdtguard installed. Initialize traffic accounting before starting the daemon:
  cdtguard init [current-usage] [-limit 195G] [-iface eth0] [-mode max]
Then start it with:
  systemctl enable --now cdtguard   (systemd)
  rc-update add cdtguard default && rc-service cdtguard start   (OpenRC)
EOF
