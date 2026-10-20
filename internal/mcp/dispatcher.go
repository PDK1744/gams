package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

// Dispatcher executes MCP JSON-RPC 2.0 requests against the GAMS engine.
type Dispatcher struct {
	engine  *engine.Engine
	version string
}

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(eng *engine.Engine, version string) *Dispatcher {
	return &Dispatcher{
		engine:  eng,
		version: version,
	}
}

// Dispatch processes a JSON-RPC request and returns the corresponding response.
// If req is a notification (req.IsNotification() == true), Dispatch returns nil.
func (d *Dispatcher) Dispatch(ctx context.Context, actor *types.APIKey, tenant *types.Tenant, req *Request) *Response {
	if req == nil {
		return &Response{
			JSONRPC: "2.0",
			Error: &RPCError{
				Code:    CodeInvalidRequest,
				Message: "request is nil",
			},
		}
	}

	if req.IsNotification() {
		// Per JSON-RPC 2.0, the server must not reply to notifications.
		return nil
	}

	if req.JSONRPC != "2.0" {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    CodeInvalidRequest,
				Message: "invalid jsonrpc version; must be '2.0'",
			},
		}
	}

	switch req.Method {
	case "initialize":
		var params InitializeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		protoVer := params.ProtocolVersion
		if protoVer == "" {
			protoVer = "2024-11-05"
		}
		result := InitializeResult{
			ProtocolVersion: protoVer,
			Capabilities: ServerCapabilities{
				Tools: ToolsCapability{
					ListChanged: false,
				},
				Resources: ResourcesCapability{
					Subscribe:   false,
					ListChanged: false,
				},
			},
			ServerInfo: ServerInfo{
				Name:        "gams",
				DisplayName: "GAMS",
				Description: "Governed Agent Memory Server",
				Version:     d.version,
			},
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}

	case "ping":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		tools := GetToolDefinitions()
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolsListResult{
				Tools: tools,
			},
		}

	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInvalidParams,
					Message: fmt.Sprintf("invalid tools/call params: %v", err),
				},
			}
		}

		if strings.TrimSpace(params.Name) == "" {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInvalidParams,
					Message: "tool name is required",
				},
			}
		}

		res, err := ExecuteTool(ctx, d.engine, actor, tenant, params.Name, params.Arguments)
		if err != nil {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInvalidParams,
					Message: err.Error(),
				},
			}
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  res,
		}

	case "resources/list":
		res := GetResourceDefinitions(tenant)
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ResourcesListResult{
				Resources: res,
			},
		}

	case "resources/templates/list":
		templates := GetResourceTemplates()
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ResourceTemplatesListResult{
				ResourceTemplates: templates,
			},
		}

	case "resources/read":
		var params ReadResourceParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInvalidParams,
					Message: fmt.Sprintf("invalid resources/read params: %v", err),
				},
			}
		}
		if strings.TrimSpace(params.URI) == "" {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInvalidParams,
					Message: "uri is required",
				},
			}
		}
		res, err := ReadResource(ctx, d.engine, actor, tenant, params.URI)
		if err != nil {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    CodeInvalidParams,
					Message: err.Error(),
				},
			}
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  res,
		}

	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    CodeMethodNotFound,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}
