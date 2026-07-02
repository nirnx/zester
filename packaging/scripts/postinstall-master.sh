#!/bin/sh
set -e

mkdir -p /etc/zester
mkdir -p /var/lib/zester/auth
mkdir -p /var/lib/zester/states
mkdir -p /var/lib/zester/settings

systemctl daemon-reload
# Enable but don't start — operator must configure NATS first
systemctl enable zester-master.service || true
