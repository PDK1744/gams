import test from 'node:test';
import assert from 'node:assert/strict';
import { GAMSClient } from '../dist/index.js';
import { GAMSMCPAdapter } from '../dist/mcp/index.js';

test('GAMSMCPAdapter returns all 4 standard MCP tool definitions', () => {
  const client = new GAMSClient({ apiKey: 'dummy-key' });
  const adapter = new GAMSMCPAdapter(client);

  const tools = adapter.getToolDefinitions();
  assert.equal(tools.length, 4);

  const toolNames = tools.map((t) => t.name);
  assert.ok(toolNames.includes('remember'));
  assert.ok(toolNames.includes('recall'));
  assert.ok(toolNames.includes('forget'));
  assert.ok(toolNames.includes('inspect'));

  // Verify remember tool schema
  const rememberTool = tools.find((t) => t.name === 'remember');
  assert.equal(rememberTool.inputSchema.type, 'object');
  assert.ok(rememberTool.inputSchema.required.includes('content'));
  assert.ok(rememberTool.inputSchema.required.includes('confidence'));
  assert.ok(rememberTool.inputSchema.required.includes('evidence'));
});

test('GAMSMCPAdapter handles unknown tool gracefully', async () => {
  const client = new GAMSClient({ apiKey: 'dummy-key' });
  const adapter = new GAMSMCPAdapter(client);

  const result = await adapter.handleToolCall('non_existent_tool', {});
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /Unknown tool/i);
});

test('GAMSMCPAdapter delegates tool calls to GAMSClient', async () => {
  const client = new GAMSClient({ apiKey: 'dummy-key' });

  // Stub client methods
  client.remember = async (input) => ({
    claim_id: 'claim-123',
    version: 1,
    created_at: '2026-10-20T00:00:00Z',
    status: 'created',
  });

  client.recall = async (input) => ({
    claims: [{ claim_id: 'claim-123', content: 'test content' }],
    total_matched: 1,
    query_ms: 5,
  });

  const adapter = new GAMSMCPAdapter(client);

  // Test remember
  const remRes = await adapter.handleToolCall('remember', {
    content: 'Memory statement',
    confidence: 1.0,
    evidence: [{ uri: 'uri://test', description: 'test', evidence_type: 'agent_assertion' }],
  });

  assert.equal(remRes.isError, false);
  const remData = JSON.parse(remRes.content[0].text);
  assert.equal(remData.claim_id, 'claim-123');
  assert.equal(remData.status, 'created');

  // Test recall
  const recRes = await adapter.handleToolCall('recall', { query: 'test' });
  assert.equal(recRes.isError, false);
  const recData = JSON.parse(recRes.content[0].text);
  assert.equal(recData.total_matched, 1);
  assert.equal(recData.claims[0].claim_id, 'claim-123');
});
