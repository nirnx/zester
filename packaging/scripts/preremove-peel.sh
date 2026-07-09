#!/bin/sh
set -e

# dpkg runs the OLD package's prerm on UPGRADES too, not just removals:
#   prerm remove              -> the package is being removed
#   prerm upgrade <new-ver>   -> an upgrade is in progress
# Stopping/disabling the service on an upgrade is catastrophic when the upgrade
# is driven from inside the service's own cgroup (e.g. zester's pkg module
# running apt): stopping the unit kills the whole cgroup — watchdog, peel, apt,
# dpkg — mid-unpack, leaving dpkg half-configured and the service down+disabled.
# So only act on a real removal. On an upgrade the running service is left
# untouched (postinst's start is a no-op); the running binary is updated on the
# self-update plane (rollout), never by apt.
if [ "$1" = "remove" ]; then
    if systemctl is-active --quiet zester-peel.service 2>/dev/null; then
        systemctl stop zester-peel.service
    fi
    systemctl disable zester-peel.service 2>/dev/null || true
fi
