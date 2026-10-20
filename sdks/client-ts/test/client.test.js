import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {
  GAMSClient,
  ValidationError,
  AuthenticationError,
  ForbiddenError,
  NotFoundError,
  ConflictError,
} from '../dist/index.js';

function createMockServer(handler) {
  return new Promise((resolve) => {
    const server = http.createServer(handler);
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      resolve({
        baseUrl: `http://127.0.0.1:${port}`,
        close: () => new Promise((res) => server.close(res)),
      });
    });
  });
}

test('GAMSClient requires apiKey', () => {
  assert.throws(() => new GAMSClient({ apiKey: '' }), ValidationError);
});

test('GAMSClient executes remember, recall, inspect, forget', async () => {
  const mock = await createMockServer((req, res) => {
    assert.equal(req.headers.authorization, 'Bearer test-token');
    res.setHeader('Content-Type', 'application/json');

    if (req.method === 'POST' && req.url === '/v1/claims') {
      res.writeHead(201);
      res.end(JSON.stringify({ claim_id: 'claim-1', version: 1, status: 'created' }));
    } else if (req.method === 'POST' && req.url === '/v1/recall') {
      res.writeHead(200);
      res.end(JSON.stringify({ claims: [{ claim_id: 'claim-1' }], total_matched: 1 }));
    } else if (req.method === 'GET' && req.url === '/v1/claims/claim-1') {
      res.writeHead(200);
      res.end(JSON.stringify({ claim: { id: 'claim-1' }, versions: [] }));
    } else if (req.method === 'POST' && req.url === '/v1/claims/claim-1/forget') {
      res.writeHead(200);
      res.end(JSON.stringify({ claim_id: 'claim-1', forgotten_at: '2026-10-20T00:00:00Z' }));
    } else {
      res.writeHead(404);
      res.end(JSON.stringify({ error: 'not found' }));
    }
  });

  try {
    const client = new GAMSClient({ baseUrl: mock.baseUrl, apiKey: 'test-token' });

    // Remember
    const rem = await client.remember({
      content: 'Hello memory',
      confidence: 1.0,
      evidence: [{ uri: 'test://uri', description: 'test', evidence_type: 'human' }],
    });
    assert.equal(rem.claim_id, 'claim-1');
    assert.equal(rem.status, 'created');

    // Recall
    const rec = await client.recall({ query: 'Hello' });
    assert.equal(rec.total_matched, 1);

    // Inspect
    const insp = await client.inspect('claim-1');
    assert.equal(insp.claim.id, 'claim-1');

    // Forget
    const forg = await client.forget('claim-1', 'obsolete');
    assert.equal(forg.claim_id, 'claim-1');
  } finally {
    await mock.close();
  }
});

test('GAMSClient maps HTTP error codes to specialized error classes', async () => {
  let nextStatus = 400;
  const mock = await createMockServer((req, res) => {
    res.setHeader('Content-Type', 'application/json');
    res.writeHead(nextStatus);
    res.end(JSON.stringify({ error: 'simulated error' }));
  });

  try {
    const client = new GAMSClient({ baseUrl: mock.baseUrl, apiKey: 'test-token' });

    nextStatus = 400;
    await assert.rejects(() => client.recall({ query: 'x' }), ValidationError);

    nextStatus = 401;
    await assert.rejects(() => client.recall({ query: 'x' }), AuthenticationError);

    nextStatus = 403;
    await assert.rejects(() => client.recall({ query: 'x' }), ForbiddenError);

    nextStatus = 404;
    await assert.rejects(() => client.recall({ query: 'x' }), NotFoundError);

    nextStatus = 409;
    await assert.rejects(() => client.recall({ query: 'x' }), ConflictError);
  } finally {
    await mock.close();
  }
});

test('GAMSClient ensureNamespace returns existing namespace or creates new one', async () => {
  let created = false;
  const mock = await createMockServer((req, res) => {
    res.setHeader('Content-Type', 'application/json');

    if (req.method === 'GET' && req.url === '/v1/namespaces/existing-ns') {
      res.writeHead(200);
      res.end(JSON.stringify({ id: 'ns-1', slug: 'existing-ns', name: 'Existing NS' }));
      return;
    }

    if (req.method === 'GET' && req.url === '/v1/namespaces/new-ns') {
      if (!created) {
        res.writeHead(404);
        res.end(JSON.stringify({ error: 'namespace_not_found' }));
      } else {
        res.writeHead(200);
        res.end(JSON.stringify({ id: 'ns-2', slug: 'new-ns', name: 'New NS' }));
      }
      return;
    }

    if (req.method === 'POST' && req.url === '/v1/namespaces') {
      created = true;
      res.writeHead(201);
      res.end(JSON.stringify({ id: 'ns-2', slug: 'new-ns', name: 'New NS' }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    const client = new GAMSClient({ baseUrl: mock.baseUrl, apiKey: 'test-token' });

    // Existing namespace: GET only
    const ns1 = await client.ensureNamespace('existing-ns');
    assert.equal(ns1.slug, 'existing-ns');

    // New namespace: GET 404 -> POST 201
    const ns2 = await client.ensureNamespace('new-ns', { name: 'New NS' });
    assert.equal(ns2.slug, 'new-ns');
    assert.equal(created, true);
  } finally {
    await mock.close();
  }
});
