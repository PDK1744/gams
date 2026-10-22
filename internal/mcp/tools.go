package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

// GetToolDefinitions returns the JSON schema specifications for all agent-facing MCP tools.
func GetToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "remember",
			Description: "Store or update a claim in the memory of the calling agent's tenant.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "The claim statement in plain text",
					},
					"confidence": map[string]any{
						"type":        "number",
						"minimum":     0.0,
						"maximum":     1.0,
						"description": "Confidence score between 0.0 and 1.0 (1.0 = verified fact, 0.5 = working assumption)",
					},
					"evidence": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"uri": map[string]any{
									"type":        "string",
									"description": "Source URI (URL, file path, tool output ID)",
								},
								"description": map[string]any{
									"type":        "string",
									"description": "Description of the source",
								},
								"evidence_type": map[string]any{
									"type": "string",
									"enum": []string{"url", "file", "tool_output", "agent_assertion", "human"},
								},
								"captured_at": map[string]any{
									"type":        "string",
									"description": "ISO8601 timestamp",
								},
							},
							"required": []string{"uri", "description", "evidence_type"},
						},
						"minItems": 1,
					},
					"contradicts": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Array of claim IDs being contradicted by this new claim",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Flat list of string tags for categorization",
					},
					"namespace": map[string]any{
						"type":        "string",
						"description": "Namespace slug (e.g. project-alpha). If omitted, writes to tenant's default namespace.",
					},
					"base_version": map[string]any{
						"type":        "integer",
						"description": "Base version for optimistic concurrency check",
					},
					"change_reason": map[string]any{
						"type":        "string",
						"description": "Optional rationale for updating the claim",
					},
					"metadata": map[string]any{
						"type":        "object",
						"description": "Arbitrary agent-defined key/value attributes",
					},
				},
				"required": []string{"content", "confidence", "evidence"},
			},
		},
		{
			Name:        "recall",
			Description: "Retrieve the most relevant claims for a query using hybrid text relevance, confidence, and recency scoring.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language or keyword query",
					},
					"limit": map[string]any{
						"type":        "integer",
						"default":     10,
						"maximum":     100,
						"description": "Maximum number of claims to return",
					},
					"confidence_min": map[string]any{
						"type":        "number",
						"minimum":     0.0,
						"maximum":     1.0,
						"default":     0.0,
						"description": "Minimum confidence threshold",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Filter by tags (AND matching)",
					},
					"namespace": map[string]any{
						"type":        "string",
						"description": "Namespace slug. If omitted, searches across all accessible namespaces.",
					},
					"include_contradicted": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "Whether to include contradicted claims",
					},
					"include_forgotten": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "Whether to include forgotten claims",
					},
					"recency_window_days": map[string]any{
						"type":        "integer",
						"description": "Exclude claims older than N days (0 = no limit)",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "forget",
			Description: "Mark a claim as forgotten (soft delete). Removes claim from index while preserving audit history.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"claim_id": map[string]any{
						"type":        "string",
						"description": "UUID of the claim to forget",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Reason why this claim is being forgotten",
					},
				},
				"required": []string{"claim_id", "reason"},
			},
		},
		{
			Name:        "inspect",
			Description: "Retrieve a single claim's full governance history, versions, evidence, and audit trail.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"claim_id": map[string]any{
						"type":        "string",
						"description": "UUID of the claim to inspect",
					},
				},
				"required": []string{"claim_id"},
			},
		},
	}
}

// ExecuteTool dispatches tool invocations to the core engine.
func ExecuteTool(ctx context.Context, eng *engine.Engine, actor *types.APIKey, tenant *types.Tenant, name string, args json.RawMessage) (*CallToolResult, error) {
	switch name {
	case "remember":
		var req types.RememberRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return makeErrorResult(fmt.Sprintf("invalid arguments for remember: %v", err)), nil
		}

		resp, err := eng.Remember(ctx, actor, tenant, &req)
		if err != nil {
			if errorsIsConflict(err) && resp != nil {
				return makeJSONResult(resp, false)
			}
			return makeErrorResult(err.Error()), nil
		}
		return makeJSONResult(resp, false)

	case "recall":
		var req types.RecallRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return makeErrorResult(fmt.Sprintf("invalid arguments for recall: %v", err)), nil
		}

		resp, err := eng.Recall(ctx, actor, tenant, &req)
		if err != nil {
			return makeErrorResult(err.Error()), nil
		}
		return makeJSONResult(resp, false)

	case "forget":
		var req struct {
			ClaimID string `json:"claim_id"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return makeErrorResult(fmt.Sprintf("invalid arguments for forget: %v", err)), nil
		}

		resp, err := eng.Forget(ctx, actor, tenant, req.ClaimID, req.Reason)
		if err != nil {
			return makeErrorResult(err.Error()), nil
		}
		return makeJSONResult(resp, false)

	case "inspect":
		var req struct {
			ClaimID string `json:"claim_id"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return makeErrorResult(fmt.Sprintf("invalid arguments for inspect: %v", err)), nil
		}
		if req.ClaimID == "" {
			return makeErrorResult("claim_id is required"), nil
		}

		resp, err := eng.Inspect(ctx, actor, tenant, req.ClaimID)
		if err != nil {
			return makeErrorResult(err.Error()), nil
		}
		return makeJSONResult(resp, false)

	default:
		return nil, fmt.Errorf("unknown tool: %q", name)
	}
}

func makeJSONResult(v any, isError bool) (*CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("serializing tool output: %w", err)
	}
	return &CallToolResult{
		Content: []TextContent{
			{
				Type: "text",
				Text: string(b),
			},
		},
		IsError: isError,
	}, nil
}

func makeErrorResult(msg string) *CallToolResult {
	return &CallToolResult{
		Content: []TextContent{
			{
				Type: "text",
				Text: msg,
			},
		},
		IsError: true,
	}
}

func errorsIsConflict(err error) bool {
	return err != nil && (errors.Is(err, engine.ErrConflict) || err.Error() == "conflict")
}
