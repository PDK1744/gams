package stdiotransport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/pkg/types"
)

const (
	// maxStdioMessageSize limits stdio payloads to 2MB, consistent with the HTTP transport body limit.
	maxStdioMessageSize = 2 * 1024 * 1024
	// maxHeaderLines caps the header search loop to avoid unbounded reading on malformed streams.
	maxHeaderLines = 50
)

// Handler implements the stdio transport for the Model Context Protocol (MCP).
// It reads JSON-RPC 2.0 requests from stdin, dispatches them through mcp.Dispatcher,
// and writes responses to stdout delimited by newlines.
type Handler struct {
	dispatcher *mcp.Dispatcher
	actor      *types.APIKey
	tenant     *types.Tenant
	logger     *slog.Logger
	writeMu    sync.Mutex
}

// NewHandler creates a new stdio transport handler.
func NewHandler(dispatcher *mcp.Dispatcher, actor *types.APIKey, tenant *types.Tenant, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		dispatcher: dispatcher,
		actor:      actor,
		tenant:     tenant,
		logger:     logger,
	}
}

// Run listens for incoming JSON-RPC messages on in and writes responses to out.
// It runs until in reaches EOF, ctx is cancelled, or an unrecoverable I/O error occurs.
func (h *Handler) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 2*1024*1024) // 2MB buffer support
	writer := bufio.NewWriter(out)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if len(strings.TrimSpace(line)) > 0 {
					_ = h.processMessage(ctx, strings.TrimSpace(line), writer)
				}
				return nil
			}
			return fmt.Errorf("reading stdio input: %w", err)
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Support LSP-style "Content-Length: <n>" framing if sent by the MCP host
		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				length, parseErr := strconv.Atoi(strings.TrimSpace(parts[1]))
				if parseErr == nil && length > 0 {
					if length > maxStdioMessageSize {
						return fmt.Errorf("stdio payload size %d exceeds maximum limit of %d bytes", length, maxStdioMessageSize)
					}
					// Read header terminator (empty line) with bounded loop
					headerCount := 0
					for {
						headerLine, hErr := reader.ReadString('\n')
						if hErr != nil {
							return hErr
						}
						if strings.TrimSpace(headerLine) == "" {
							break
						}
						headerCount++
						if headerCount > maxHeaderLines {
							return fmt.Errorf("exceeded maximum header line limit (%d) without empty line separator", maxHeaderLines)
						}
					}
					// Read exact payload bytes
					buf := make([]byte, length)
					if _, rErr := io.ReadFull(reader, buf); rErr != nil {
						return rErr
					}
					trimmed = strings.TrimSpace(string(buf))
				}
			}
		}

		if trimmed == "" {
			continue
		}

		if err := h.processMessage(ctx, trimmed, writer); err != nil {
			h.logger.Error("error processing stdio message", "error", err)
		}
	}
}

func (h *Handler) processMessage(ctx context.Context, raw string, writer *bufio.Writer) error {
	// 1. Batch requests: JSON array [req1, req2]
	if raw[0] == '[' {
		var reqs []mcp.Request
		if err := json.Unmarshal([]byte(raw), &reqs); err != nil {
			return h.writeRaw(writer, &mcp.Response{
				JSONRPC: "2.0",
				Error: &mcp.RPCError{
					Code:    mcp.CodeParseError,
					Message: "parse error: invalid JSON array",
				},
			})
		}

		if len(reqs) == 0 {
			return h.writeRaw(writer, &mcp.Response{
				JSONRPC: "2.0",
				Error: &mcp.RPCError{
					Code:    mcp.CodeInvalidRequest,
					Message: "invalid request: empty batch array",
				},
			})
		}

		var responses []*mcp.Response
		for i := range reqs {
			resp := h.dispatcher.Dispatch(ctx, h.actor, h.tenant, &reqs[i])
			if resp != nil {
				responses = append(responses, resp)
			}
		}

		if len(responses) > 0 {
			return h.writeRaw(writer, responses)
		}
		return nil
	}

	// 2. Single request
	var req mcp.Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return h.writeRaw(writer, &mcp.Response{
			JSONRPC: "2.0",
			Error: &mcp.RPCError{
				Code:    mcp.CodeParseError,
				Message: "parse error: invalid JSON",
			},
		})
	}

	resp := h.dispatcher.Dispatch(ctx, h.actor, h.tenant, &req)
	if resp != nil {
		return h.writeRaw(writer, resp)
	}

	// Notifications yield no response
	return nil
}

func (h *Handler) writeRaw(writer *bufio.Writer, payload any) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling stdio response: %w", err)
	}

	if _, err := writer.Write(b); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}
