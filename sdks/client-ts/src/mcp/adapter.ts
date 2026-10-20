import type { GAMSClient } from '../client.js';
import type {
  MCPToolDefinition,
  MCPToolResult,
  RememberInput,
  RecallInput,
} from '../types.js';

export class GAMSMCPAdapter {
  constructor(private readonly client: GAMSClient) {}

  /**
   * Returns JSON Schema specifications for all GAMS memory tools.
   */
  public getToolDefinitions(): MCPToolDefinition[] {
    return [
      {
        name: 'remember',
        description: 'Store or update a claim in the memory of the calling agent’s tenant.',
        inputSchema: {
          type: 'object',
          properties: {
            content: {
              type: 'string',
              description: 'The claim statement in plain text',
            },
            confidence: {
              type: 'number',
              minimum: 0.0,
              maximum: 1.0,
              description: 'Confidence score between 0.0 and 1.0 (1.0 = verified fact, 0.5 = working assumption)',
            },
            evidence: {
              type: 'array',
              items: {
                type: 'object',
                properties: {
                  uri: { type: 'string', description: 'Source URI (URL, file path, tool output ID)' },
                  description: { type: 'string', description: 'Description of the source' },
                  evidence_type: {
                    type: 'string',
                    enum: ['url', 'file', 'tool_output', 'agent_assertion', 'human'],
                  },
                  captured_at: { type: 'string', description: 'ISO8601 timestamp' },
                },
                required: ['uri', 'description', 'evidence_type'],
              },
              minItems: 1,
            },
            contradicts: {
              type: 'array',
              items: { type: 'string' },
              description: 'Array of claim IDs being contradicted by this new claim',
            },
            tags: {
              type: 'array',
              items: { type: 'string' },
              description: 'Flat list of string tags for categorization',
            },
            namespace: {
              type: 'string',
              description: 'Namespace slug. If omitted, writes to tenant default namespace.',
            },
            claim_id: {
              type: 'string',
              description: 'Existing claim UUID if updating a claim',
            },
            base_version: {
              type: 'integer',
              description: 'Base version for optimistic concurrency check',
            },
            change_reason: {
              type: 'string',
              description: 'Optional rationale for updating the claim',
            },
            metadata: {
              type: 'object',
              description: 'Arbitrary agent-defined key/value attributes',
            },
          },
          required: ['content', 'confidence', 'evidence'],
        },
      },
      {
        name: 'recall',
        description: 'Retrieve the most relevant claims for a query using hybrid text relevance, confidence, and recency scoring.',
        inputSchema: {
          type: 'object',
          properties: {
            query: {
              type: 'string',
              description: 'Natural language or keyword query',
            },
            limit: {
              type: 'integer',
              default: 10,
              maximum: 100,
              description: 'Maximum number of claims to return',
            },
            confidence_min: {
              type: 'number',
              minimum: 0.0,
              maximum: 1.0,
              default: 0.0,
              description: 'Minimum confidence threshold',
            },
            tags: {
              type: 'array',
              items: { type: 'string' },
              description: 'Filter by tags (AND matching)',
            },
            namespace: {
              type: 'string',
              description: 'Namespace slug. If omitted, searches across all accessible namespaces.',
            },
            include_contradicted: {
              type: 'boolean',
              default: false,
              description: 'Whether to include contradicted claims',
            },
            include_forgotten: {
              type: 'boolean',
              default: false,
              description: 'Whether to include forgotten claims',
            },
            recency_window_days: {
              type: 'integer',
              description: 'Exclude claims older than N days (0 = no limit)',
            },
          },
          required: ['query'],
        },
      },
      {
        name: 'forget',
        description: 'Mark a claim as forgotten (soft delete). Removes claim from index while preserving audit history.',
        inputSchema: {
          type: 'object',
          properties: {
            claim_id: {
              type: 'string',
              description: 'UUID of the claim to forget',
            },
            reason: {
              type: 'string',
              description: 'Reason why this claim is being forgotten',
            },
          },
          required: ['claim_id', 'reason'],
        },
      },
      {
        name: 'inspect',
        description: 'Retrieve a single claim’s full governance history, versions, evidence, and audit trail.',
        inputSchema: {
          type: 'object',
          properties: {
            claim_id: {
              type: 'string',
              description: 'UUID of the claim to inspect',
            },
          },
          required: ['claim_id'],
        },
      },
    ];
  }

  /**
   * Executes an MCP tool call against the underlying GAMSClient.
   */
  public async handleToolCall(
    name: string,
    args: Record<string, any> = {}
  ): Promise<MCPToolResult> {
    try {
      let result: any;

      switch (name) {
        case 'remember':
          result = await this.client.remember(args as RememberInput);
          break;
        case 'recall':
          result = await this.client.recall(args as RecallInput);
          break;
        case 'forget':
          result = await this.client.forget(args.claim_id, args.reason);
          break;
        case 'inspect':
          result = await this.client.inspect(args.claim_id);
          break;
        default:
          return {
            content: [{ type: 'text', text: `Unknown tool: ${name}` }],
            isError: true,
          };
      }

      return {
        content: [{ type: 'text', text: JSON.stringify(result) }],
        isError: false,
      };
    } catch (err: any) {
      return {
        content: [{ type: 'text', text: err?.message || String(err) }],
        isError: true,
      };
    }
  }
}

export default GAMSMCPAdapter;
