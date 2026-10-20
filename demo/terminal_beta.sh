#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -f "${SCRIPT_DIR}/keys.env" ]; then
    echo "Error: demo/keys.env not found. Please run demo/setup_demo.sh first."
    exit 1
fi
source "${SCRIPT_DIR}/keys.env"

echo "=========================================================="
echo " [TERMINAL 2] AGENT BETA (Backend Specialist)"
echo " Connecting to GAMS via MCP HTTP/SSE..."
echo "=========================================================="

python3 "${SCRIPT_DIR}/agent.py" \
  --name "Agent-Beta" \
  --url "${GAMS_URL}" \
  --key "${KEY_BETA}" \
  --namespace "project-collab" \
  --remember "Backend architecture decision: Adopted PostgreSQL 17 distributed database with pgvector for embeddings." \
  --confidence 0.92 \
  --evidence-uri "doc://be-rfc-02" \
  --evidence-desc "Backend Architecture RFC 02" \
  --tags "backend" "database" "postgres" \
  --sleep-after-write 1.0 \
  --recall "frontend Next.js React"
