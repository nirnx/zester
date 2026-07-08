#!/bin/sh
set -e

# Create directories
mkdir -p /etc/zester
mkdir -p /var/lib/zester/auth
mkdir -p /var/cache/zester/states

# Derive a valid peel ID from the hostname: enrollment requires
# ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ (max 128 chars) — no dots, so FQDN hostnames
# are cut at the first label and invalid characters become '-'.
PEEL_ID="$(hostname -s 2>/dev/null || hostname | cut -d. -f1)"
PEEL_ID="$(printf '%s' "$PEEL_ID" | tr -c 'a-zA-Z0-9_-' '-' | sed 's/^[_-]*//' | cut -c1-128)"
if [ -z "$PEEL_ID" ]; then
    PEEL_ID="peel-$(date +%s)"
fi

# Set default ID on first install (preserved on upgrades).
if [ -f /etc/zester/peel.yaml ] && grep -q '^id: ""' /etc/zester/peel.yaml; then
    sed -i "s/^id: \"\"/id: \"$PEEL_ID\"/" /etc/zester/peel.yaml
fi

# Freeze the same ID for the watchdog unit (ZESTER_PEEL_ID drives --id and
# the --nats-creds <id>.creds path). Written once so a later hostname change
# cannot desync the unit from the id recorded in peel.yaml.
if [ ! -f /etc/systemd/system/zester-peel.service.d/10-peel-id.conf ]; then
    mkdir -p /etc/systemd/system/zester-peel.service.d
    printf '[Service]\nEnvironment=ZESTER_PEEL_ID=%s\n' "$PEEL_ID" \
        > /etc/systemd/system/zester-peel.service.d/10-peel-id.conf
fi

systemctl daemon-reload
systemctl enable zester-peel.service || true
systemctl start zester-peel.service || true
