#!/bin/sh
set -e

# dpkg runs the OLD package's prerm on UPGRADES too, not just removals
# (prerm remove | prerm upgrade <new-ver>). Only stop/disable on a real
# removal — a package upgrade must never touch the running service. See
# preremove-peel.sh for the full rationale (self-cgroup kill on pkg-driven
# upgrades). The master survived earlier apt upgrades only because apt ran
# from an SSH session outside the service cgroup.
if [ "$1" = "remove" ]; then
    if systemctl is-active --quiet zester-master.service 2>/dev/null; then
        systemctl stop zester-master.service
    fi
    systemctl disable zester-master.service 2>/dev/null || true
fi
