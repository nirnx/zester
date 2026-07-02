#!/bin/sh
set -e

# Create directories
mkdir -p /etc/zester
mkdir -p /var/lib/zester/auth
mkdir -p /var/lib/zester/states-cache

# Set default ID to hostname on first install
if [ -f /etc/zester/peel.yaml ] && grep -q '^id: ""' /etc/zester/peel.yaml; then
    sed -i "s/^id: \"\"/id: \"$(hostname)\"/" /etc/zester/peel.yaml
fi

systemctl daemon-reload
systemctl enable zester-peel.service || true
systemctl start zester-peel.service || true
