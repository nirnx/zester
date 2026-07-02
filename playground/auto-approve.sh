#!/bin/sh
# Auto-approve all pending enrollment requests.
# Usage: docker compose exec admin sh /playground/auto-approve.sh

set -e

PENDING=$(zester enroll list --state pending 2>/dev/null | grep '^enr-' | awk '{print $1}')

if [ -z "$PENDING" ]; then
    echo "No pending enrollments found."
    exit 0
fi

for id in $PENDING; do
    echo "Approving $id ..."
    zester enroll approve "$id"
done

echo "Done. Approved $(echo "$PENDING" | wc -l | tr -d ' ') enrollment(s)."
