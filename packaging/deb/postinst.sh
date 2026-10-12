#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	systemctl enable cdtguard.service || true
	# Fails harmlessly on hosts with several NICs until -iface is set in /etc/default/cdtguard.
	systemctl restart cdtguard.service || echo "cdtguard: failed to start; check /etc/default/cdtguard (e.g. set -iface)" >&2
fi
