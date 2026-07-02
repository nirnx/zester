#!/bin/sh
set -e
if systemctl is-active --quiet zester-master.service 2>/dev/null; then
    systemctl stop zester-master.service
fi
systemctl disable zester-master.service 2>/dev/null || true
