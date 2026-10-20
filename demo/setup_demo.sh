#!/usr/bin/env bash
set -euo pipefail

ADMIN_KEY="demo-admin-key"
PORT=8080
BASE_URL="http://127.0.0.1:${PORT}"
DB_PATH="demo_gams.db"

echo "=== GAMS Phase 2 Demo Setup ==="

# 1. Build binary
if [ ! -f "./gams" ]; then
    echo "Building GAMS binary..."
    go build -ldflags="-X 'main.Version=0.1.0' -s -w" -o gams ./cmd/gams
fi

# Clean previous demo DB if present
rm -f "${DB_PATH}" "${DB_PATH}-wal" "${DB_PATH}-shm"

# 2. Start GAMS server in background if not already running
if curl -s "${BASE_URL}/health" >/dev/null 2>&1; then
    echo "GAMS server is already running on ${BASE_URL}."
else
    echo "Starting GAMS server on ${BASE_URL}..."
    GAMS_DATA_PATH="${DB_PATH}" \
    GAMS_SERVER_HTTP_PORT="${PORT}" \
    GAMS_SERVER_ADMIN_KEY="${ADMIN_KEY}" \
    ./gams serve > demo_server.log 2>&1 &
    SERVER_PID=$!
    echo $SERVER_PID > demo_server.pid

    # Wait for server to become healthy
    for i in {1..30}; do
        if curl -s "${BASE_URL}/health" | grep -q '"status":"ok"'; then
            echo "GAMS server is healthy (PID ${SERVER_PID})."
            break
        fi
        sleep 0.2
    done
fi

# 3. Create Tenant
echo "Creating tenant 'Multi-Agent Collective'..."
TENANT_RESP=$(curl -s -X POST "${BASE_URL}/admin/tenants" \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"name": "Multi-Agent Collective", "auto_create_namespaces": true}')

TENANT_ID=$(echo "${TENANT_RESP}" | grep -o '"id":"[^"]*' | head -1 | cut -d'"' -f4)
echo "Tenant created: ${TENANT_ID}"

# 4. Create shared namespace
echo "Creating shared namespace 'project-collab'..."
curl -s -X POST "${BASE_URL}/admin/tenants/${TENANT_ID}/namespaces" \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"slug": "project-collab", "name": "Shared Collaboration Space", "access_mode": "tenant_wide"}' > /dev/null

# 5. Issue API Keys for Agent Alpha and Agent Beta
echo "Issuing API key for Agent Alpha..."
KEY_ALPHA_RESP=$(curl -s -X POST "${BASE_URL}/admin/tenants/${TENANT_ID}/keys" \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"label": "agent-alpha"}')
KEY_ALPHA=$(echo "${KEY_ALPHA_RESP}" | grep -o '"key":"[^"]*' | cut -d'"' -f4)

echo "Issuing API key for Agent Beta..."
KEY_BETA_RESP=$(curl -s -X POST "${BASE_URL}/admin/tenants/${TENANT_ID}/keys" \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"label": "agent-beta"}')
KEY_BETA=$(echo "${KEY_BETA_RESP}" | grep -o '"key":"[^"]*' | cut -d'"' -f4)

# 6. Save environment file for terminal sessions
cat <<EOF > demo/keys.env
export GAMS_URL="${BASE_URL}"
export TENANT_ID="${TENANT_ID}"
export KEY_ALPHA="${KEY_ALPHA}"
export KEY_BETA="${KEY_BETA}"
EOF

echo "Saved demo credentials to demo/keys.env."
echo ""
echo "=== Setup Complete! ==="
echo "Agent Alpha Key: ${KEY_ALPHA}"
echo "Agent Beta Key:  ${KEY_BETA}"
echo "Namespace:       project-collab"
