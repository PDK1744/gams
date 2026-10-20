#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -f "${SCRIPT_DIR}/keys.env" ]; then
    echo "Error: demo/keys.env not found. Please run demo/setup_demo.sh first."
    exit 1
fi
source "${SCRIPT_DIR}/keys.env"

echo "=========================================================="
echo " [TERMINAL 1] AGENT ALPHA (Frontend Specialist)"
echo " Connecting to GAMS via MCP HTTP/SSE..."
echo "=========================================================="

python3 "${SCRIPT_DIR}/agent.py" \
  --name "Agent-Alpha" \
  --url "${GAMS_URL}" \
  --key "${KEY_ALPHA}" \
  --namespace "project-collab" \
  --remember "Frontend architecture decision: Adopted Next.js 15 App Router with Tailwind CSS and React Server Components." \
  --confidence 0.95 \
  --evidence-uri "doc://fe-rfc-01" \
  --evidence-desc "Frontend Architecture RFC 01" \
  --tags "frontend" "nextjs" "react" \
  --sleep-after-write 3.0 \
  --recall "backend database PostgreSQL"
