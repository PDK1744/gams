import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import path from 'node:path';
import fs from 'node:fs';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { GAMSClient } from '../dist/index.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const GAMS_BIN = path.resolve(__dirname, '../../../gams');

function runStdioSession(dbPath, port, requests) {
  return new Promise((resolve, reject) => {
    const child = spawn(
      GAMS_BIN,
      ['serve', '--transport', 'both'],
      {
        env: {
          ...process.env,
          GAMS_DATA_PATH: dbPath,
          GAMS_SERVER_HTTP_PORT: String(port),
          GAMS_SERVER_ADMIN_KEY: 'test-admin',
          GAMS_DEV: 'true',
        },
        stdio: ['pipe', 'pipe', 'pipe'],
      }
    );

    let stdoutData = '';
    let stderrData = '';

    child.stdout.on('data', (d) => {
      stdoutData += d.toString();
    });
    child.stderr.on('data', (d) => {
      stderrData += d.toString();
    });

    child.on('error', reject);

    // Send requests line by line
    for (const req of requests) {
      child.stdin.write(JSON.stringify(req) + '\n');
    }
    child.stdin.end();

    child.on('close', (code) => {
      const lines = stdoutData
        .split('\n')
        .map((l) => l.trim())
        .filter(Boolean);

      const responses = lines.map((l) => {
        try {
          return JSON.parse(l);
        } catch {
          return null;
        }
      }).filter(Boolean);

      resolve({ code, responses, stderr: stderrData });
    });
  });
}

test('Claude Code stdio session persistence and @gams/client REST interoperability', async () => {
  if (!fs.existsSync(GAMS_BIN)) {
    console.log('Skipping interop test: gams binary not found at', GAMS_BIN);
    return;
  }

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gams-interop-'));
  const dbPath = path.join(tmpDir, 'interop.db');
  const port = 8991;

  try {
    // -------------------------------------------------------------
    // Session 1: Claude Code connects via stdio, writes a claim, and exits.
    // -------------------------------------------------------------
    const session1Requests = [
      {
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: {
          protocolVersion: '2024-11-05',
          clientInfo: { name: 'claude-code', version: '1.0' },
        },
      },
      {
        jsonrpc: '2.0',
        method: 'notifications/initialized',
      },
      {
        jsonrpc: '2.0',
        id: 2,
        method: 'tools/call',
        params: {
          name: 'remember',
          arguments: {
            content: 'Architecture Decision: Auth service uses OAuth2 with PKCE flow',
            confidence: 0.95,
            evidence: [
              {
                uri: 'doc://auth-rfc',
                description: 'Authentication RFC',
                evidence_type: 'agent_assertion',
              },
            ],
            tags: ['auth', 'architecture', 'oauth2'],
          },
        },
      },
    ];

    const s1 = await runStdioSession(dbPath, port, session1Requests);
    assert.equal(s1.responses.length, 2, `Expected 2 responses from s1, got: ${JSON.stringify(s1.responses)}`);

    const rememberRes = s1.responses[1].result;
    assert.ok(rememberRes, 'Missing remember result');
    const claimData = JSON.parse(rememberRes.content[0].text);
    const claimId = claimData.claim_id;
    assert.ok(claimId, 'Claim ID must be generated');

    // -------------------------------------------------------------
    // Session 2: Claude Code starts a NEW session via stdio, recalls memory.
    // -------------------------------------------------------------
    const session2Requests = [
      {
        jsonrpc: '2.0',
        id: 10,
        method: 'initialize',
        params: {
          protocolVersion: '2024-11-05',
          clientInfo: { name: 'claude-code', version: '1.0' },
        },
      },
      {
        jsonrpc: '2.0',
        id: 11,
        method: 'tools/call',
        params: {
          name: 'recall',
          arguments: {
            query: 'OAuth2 PKCE flow authentication',
          },
        },
      },
    ];

    const s2 = await runStdioSession(dbPath, port, session2Requests);
    assert.equal(s2.responses.length, 2);

    const recallRes = s2.responses[1].result;
    const recallData = JSON.parse(recallRes.content[0].text);
    assert.equal(recallData.total_matched, 1);
    assert.equal(recallData.claims[0].claim_id, claimId);
    assert.match(recallData.claims[0].content, /OAuth2 with PKCE flow/);

    // -------------------------------------------------------------
    // Session 3: Standalone Node.js app uses @gams/client REST to read same memory!
    // -------------------------------------------------------------
    // First, start GAMS server in background with HTTP enabled
    const serverProc = spawn(
      GAMS_BIN,
      ['serve', '--transport', 'http'],
      {
        env: {
          ...process.env,
          GAMS_DATA_PATH: dbPath,
          GAMS_SERVER_HTTP_PORT: String(port),
          GAMS_SERVER_ADMIN_KEY: 'test-admin',
          GAMS_DEV: 'true',
        },
        stdio: ['ignore', 'pipe', 'pipe'],
      }
    );

    // Wait for server /health
    let healthy = false;
    for (let i = 0; i < 30; i++) {
      try {
        const res = await fetch(`http://127.0.0.1:${port}/health`);
        if (res.ok) {
          healthy = true;
          break;
        }
      } catch {}
      await new Promise((r) => setTimeout(r, 100));
    }
    assert.ok(healthy, 'Server failed to start on HTTP');

    try {
      // Find the local bootstrap API key created in the DB
      // We can issue a new agent key via admin API or read the local tenant
      const adminRes = await fetch(`http://127.0.0.1:${port}/admin/tenants/local/keys`, {
        method: 'POST',
        headers: {
          Authorization: 'Bearer test-admin',
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ label: 'test-node-app' }),
      });
      assert.equal(adminRes.status, 201);
      const keyData = await adminRes.json();
      const apiKey = keyData.key;

      const client = new GAMSClient({
        baseUrl: `http://127.0.0.1:${port}`,
        apiKey,
      });

      // Recall memory stored by Claude Code via REST
      const restRecall = await client.recall({ query: 'OAuth2 PKCE flow' });
      assert.equal(restRecall.total_matched, 1);
      assert.equal(restRecall.claims[0].claim_id, claimId);

      // Inspect full lineage and evidence via REST
      const inspectRes = await client.inspect(claimId);
      assert.equal(inspectRes.claim.id, claimId);
      assert.equal(inspectRes.evidence.length, 1);
      assert.equal(inspectRes.evidence[0].uri, 'doc://auth-rfc');

      console.log('PASS: Successfully proved stdio <-> REST interoperability!');
    } finally {
      serverProc.kill();
    }
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});
