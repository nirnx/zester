#!/bin/sh
set -e

# Create directories
mkdir -p /etc/zester
mkdir -p /var/lib/zester/auth
mkdir -p /var/cache/zester/states

# The peel id is now resolved at runtime by zester-peel/zester-watchdog: the
# 'id' in peel.yaml if set, otherwise the machine hostname (`hostname -f`,
# sanitized into a valid NATS subject token — dots become '_', other invalid
# chars become '-'). Nothing to derive or freeze at install time.

systemctl daemon-reload
systemctl enable zester-peel.service || true

# Deliberately NOT started here. An unconfigured peel (no master_urls) would
# fall back to the convention master https://zester:8443 and, with the default
# TOFU trust mode, could silently self-enroll into a half-state before the
# operator has set master_urls / enroll_ca_pin. Configure /etc/zester/peel.yaml
# first, then:  systemctl start zester-peel
if grep -qE '^[[:space:]]*master_urls:' /etc/zester/peel.yaml 2>/dev/null; then
    # An operator-provided config already sets master_urls — safe to start.
    systemctl start zester-peel.service || true
else
    echo "zester-peel installed but NOT started: set master_urls (and optionally"
    echo "enroll_ca_pin) in /etc/zester/peel.yaml, then: systemctl start zester-peel"
fi
