#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"

echo "================================================================================"
echo "          GAMS Phase 2 Demo: Concurrent Multi-Agent Shared Memory"
echo "================================================================================"

# 1. Setup server and credentials
bash "${SCRIPT_DIR}/setup_demo.sh"

echo ""
echo "--------------------------------------------------------------------------------"
echo "Launching Two Concurrent Agent Sessions (Simulating Two Separate Terminals)"
echo "--------------------------------------------------------------------------------"

# Launch Agent Alpha in background (writes claim, then waits 3s before recall)
bash "${SCRIPT_DIR}/terminal_alpha.sh" > demo_alpha.out 2>&1 &
ALPHA_PID=$!
echo "Terminal 1 (Agent Alpha) started with PID ${ALPHA_PID}."

# Wait 1.5s for Alpha to connect and store its claim
sleep 1.5

# Launch Agent Beta in background (writes claim, then recalls)
bash "${SCRIPT_DIR}/terminal_beta.sh" > demo_beta.out 2>&1 &
BETA_PID=$!
echo "Terminal 2 (Agent Beta) started with PID ${BETA_PID}."

# Wait for both agents to complete their workflows
wait $ALPHA_PID
wait $BETA_PID

echo ""
echo "================================================================================"
echo ">>> OUTPUT FROM TERMINAL 1 (Agent Alpha):"
echo "================================================================================"
cat demo_alpha.out

echo ""
echo "================================================================================"
echo ">>> OUTPUT FROM TERMINAL 2 (Agent Beta):"
echo "================================================================================"
cat demo_beta.out

echo ""
echo "================================================================================"
echo ">>> DEMO VERIFICATION:"
if grep -q "Adopted PostgreSQL" demo_alpha.out && grep -q "Adopted Next.js" demo_beta.out; then
    echo "SUCCESS: GAMS Multi-Agent Shared Memory Verified!"
    echo "  - Agent Alpha successfully recalled Agent Beta's claim."
    echo "  - Agent Beta successfully recalled Agent Alpha's claim."
    echo "  - Both agents communicated over Streamable HTTP (SSE + POST) via MCP."
else
    echo "FAILED: Claims were not mutually discovered."
    exit 1
fi
echo "================================================================================"

# Teardown background server if we started it
if [ -f demo_server.pid ]; then
    SERVER_PID=$(cat demo_server.pid)
    echo "Stopping background GAMS server (PID ${SERVER_PID})..."
    kill $SERVER_PID 2>/dev/null || true
    rm -f demo_server.pid demo_alpha.out demo_beta.out demo_gams.db*
fi
