# @gams/client

Official TypeScript SDK for the **Governed Agent Memory Server (GAMS)**.

- **ESM-only**, Node ≥ 18
- Zero external HTTP dependencies (native `fetch`)
- Full TypeScript types for all memory operations, tools, and resources
- Auto-retry with exponential backoff on 409 optimistic concurrency conflicts
- Idempotent `ensureNamespace` helper
- Includes `/mcp` submodule adapter (`GAMSMCPAdapter`) for custom MCP servers

## Installation

```bash
npm install @gams/client
```

## Quickstart

```typescript
import { GAMSClient } from '@gams/client';

const client = new GAMSClient({
  baseUrl: 'http://127.0.0.1:8080',
  apiKey: process.env.GAMS_API_KEY!,
});

// 1. Ensure namespace exists (idempotent)
await client.ensureNamespace('project-alpha', {
  name: 'Project Alpha Workspace',
});

// 2. Remember a claim
const remembered = await client.remember({
  content: 'Production database uses PostgreSQL 17 with WAL mode',
  confidence: 0.95,
  evidence: [
    {
      uri: 'doc://architecture-spec',
      description: 'Infrastructure Architecture RFC',
      evidence_type: 'file',
    },
  ],
  namespace: 'project-alpha',
  tags: ['database', 'postgres'],
});

console.log('Stored claim:', remembered.claim_id);

// 3. Recall relevant claims
const recalled = await client.recall({
  query: 'PostgreSQL database',
  namespace: 'project-alpha',
});

console.log('Matched claims:', recalled.claims);

// 4. Inspect full governance lineage and audit trail
const inspected = await client.inspect(remembered.claim_id);
console.log('Governance lineage:', inspected.versions, inspected.audit);

// 5. Soft-delete / forget a claim
await client.forget(remembered.claim_id, 'Migrated to CockroachDB');
```

## MCP Adapter Submodule (`@gams/client/mcp`)

Use the `/mcp` submodule adapter if you are building a custom MCP server and want to expose GAMS memory tools:

```typescript
import { GAMSClient } from '@gams/client';
import { GAMSMCPAdapter } from '@gams/client/mcp';

const client = new GAMSClient({ apiKey: process.env.GAMS_API_KEY! });
const adapter = new GAMSMCPAdapter(client);

// 1. Get tool JSON schemas for registration
const tools = adapter.getToolDefinitions();
// Returns schemas for 'remember', 'recall', 'forget', 'inspect'

// 2. Handle MCP tool calls
const result = await adapter.handleToolCall('remember', {
  content: 'User prefers dark mode',
  confidence: 1.0,
  evidence: [{ uri: 'ui://prefs', description: 'User settings', evidence_type: 'human' }],
});
```

## License

Apache-2.0
