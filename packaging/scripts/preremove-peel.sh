#!/bin/sh
set -e
if systemctl is-active --quiet zester-peel.service 2>/dev/null; then
    systemctl stop zester-peel.service
fi
systemctl disable zester-peel.service 2>/dev/null || true
