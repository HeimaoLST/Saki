package main

// MCP 2025-11-25 legacy stdio helper for conversation-issued Nagi issue tools.
// The helper only speaks MCP and forwards one tool call to the application IPC;
// it owns no contact/issue/store/Runtime state. The same binary enters this
// function via --issue-tool-helper; no GUI shortcut or second Runtime client
// lives here.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	issueToolMCPProtocolVersion = "2025-11-25"
	issueToolMaxFrameBytes      = 4 * 1024 * 1024
	issueToolDefaultCallTimeout = 30 * time.Second
)

var errIssueToolResponseTooLarge = errors.New("issue tool response exceeds the 4 MiB frame")

type issueToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type issueToolForwardRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type issueToolForwardResponse struct {
	Content           []issueToolContent `json:"content"`
	StructuredContent json.RawMessage    `json:"structuredContent,omitempty"`
	IsError           bool               `json:"isError,omitempty"`
}

type issueToolForwardFunc func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error)

type issueToolHelperOptions struct {
	ForwardTimeout time.Duration
	MaxFrameBytes  int
	Logger         io.Writer
}

func (o *issueToolHelperOptions) withDefaults() {
	if o.ForwardTimeout <= 0 {
		o.ForwardTimeout = issueToolDefaultCallTimeout
	}
	if o.MaxFrameBytes <= 0 {
		o.MaxFrameBytes = issueToolMaxFrameBytes
	}
	if o.Logger == nil {
		o.Logger = io.Discard
	}
}

type issueToolRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type issueToolRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type issueToolRPCResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id,omitempty"`
	Result  any                `json:"result,omitempty"`
	Error   *issueToolRPCError `json:"error,omitempty"`
}

type issueToolInitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools struct {
			ListChanged bool `json:"listChanged"`
		} `json:"tools"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type issueToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func issueToolDefinitions() []issueToolDefinition {
	stringProp := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []issueToolDefinition{
		{
			Name: "issue_list", Description: "List issues already authorized in this app-issued conversation scope. Read-only; only call after an explicit user request. Returns a bounded page with has_more/next_cursor.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"scope":            stringProp("Opaque app-issued scope token; never invent or change it."),
					"include_archived": map[string]any{"type": "boolean", "default": false},
					"limit":            map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
					"cursor":           stringProp("Opaque next_cursor from a previous page."),
				},
				"required": []string{"scope"},
			},
		},
		{
			Name: "issue_get", Description: "Read one authorized issue's goal, structured requirements, bounded executions and pending supplement count. Read-only; never infer an issue from GUI selection.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"scope":    stringProp("Opaque app-issued scope token; never invent or change it."),
					"issue_id": stringProp("Exact authorized issue id."),
				},
				"required": []string{"scope", "issue_id"},
			},
		},
		{
			Name: "issue_wait", Description: "Report that the current issue execution needs information and pause work. Returns a short waiting_user receipt with question_ref; do not wait inside the tool call.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"scope": stringProp("Opaque app-issued work scope token; never invent or change it."),
					"text":  stringProp("The missing-information question for the user."),
				},
				"required": []string{"scope", "text"},
			},
		},
		{
			Name: "issue_action", Description: "Apply one explicit user-authorized issue action through Nagi's existing issue path, including wait for missing information and resume. Do not use for ordinary discussion. Ambiguous/missing targets return candidates; no operation is accepted without real Runtime IDs.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"scope":             stringProp("Opaque app-issued scope token; never invent or change it."),
					"action":            map[string]any{"type": "string", "enum": []string{issueToolActionCreate, issueToolActionUpdate, issueToolActionRun, issueToolActionStop, issueToolActionAddSupplement, issueToolActionWait, issueToolActionResume}},
					"issue_id":          stringProp("Exact authorized issue id; omitted only for create."),
					"expected_revision": map[string]any{"type": "integer", "minimum": 1},
					"goal":              stringProp("Issue goal. Required for create/update."),
					"requirements":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"text":              stringProp("Supplement text for add_supplement; question text for wait."),
					"question_ref":      stringProp("Open question ref from a wait; required to attach an answer to that question."),
				},
				"required": []string{"scope", "action"},
			},
		},
	}
}

func issueToolInitialize() issueToolInitializeResult {
	var result issueToolInitializeResult
	result.ProtocolVersion = issueToolMCPProtocolVersion
	result.Capabilities.Tools.ListChanged = false
	result.ServerInfo.Name = "nagi-issue-tools"
	result.ServerInfo.Version = "1"
	return result
}

// issueToolHelperServe runs the legacy MCP newline-JSON lifecycle. It emits only
// JSON-RPC on out and returns after EOF/transport failure. No request is retried.
func issueToolHelperServe(ctx context.Context, in io.Reader, out io.Writer, forward issueToolForwardFunc, options issueToolHelperOptions) error {
	options.withDefaults()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), options.MaxFrameBytes)
	initialized := false
	handshakeStarted := false
	write := func(response issueToolRPCResponse) error {
		response.JSONRPC = "2.0"
		if len(response.ID) == 0 {
			response.ID = json.RawMessage("null")
		}
		data, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if len(data) > options.MaxFrameBytes {
			return errIssueToolResponseTooLarge
		}
		n, err := out.Write(append(data, '\n'))
		if err != nil {
			return err
		}
		if n != len(data)+1 {
			return io.ErrShortWrite
		}
		return nil
	}
	writeError := func(id json.RawMessage, code int, message string) error {
		return write(issueToolRPCResponse{ID: id, Error: &issueToolRPCError{Code: code, Message: message}})
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var request issueToolRPCRequest
		if err := json.Unmarshal(line, &request); err != nil {
			if writeErr := writeError(nil, -32700, "parse error"); writeErr != nil {
				return writeErr
			}
			continue
		}
		if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
			if writeErr := writeError(request.ID, -32600, "invalid request"); writeErr != nil {
				return writeErr
			}
			continue
		}
		notification := len(request.ID) == 0
		switch request.Method {
		case "initialize":
			if notification {
				if writeErr := writeError(request.ID, -32600, "initialize requires an id"); writeErr != nil {
					return writeErr
				}
				continue
			}
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if len(request.Params) > 0 {
				if err := json.Unmarshal(request.Params, &params); err != nil {
					if writeErr := writeError(request.ID, -32602, "invalid initialize params"); writeErr != nil {
						return writeErr
					}
					continue
				}
			}
			_ = params // legacy version is fixed below; no modern negotiation
			result := issueToolInitialize()
			handshakeStarted = true
			if err := write(issueToolRPCResponse{ID: request.ID, Result: result}); err != nil {
				return err
			}
		case "notifications/initialized":
			// Notifications never receive a response. This notification only
			// completes a lifecycle that began with initialize.
			if handshakeStarted {
				initialized = true
			}
		case "ping":
			if notification {
				continue
			}
			if err := write(issueToolRPCResponse{ID: request.ID, Result: map[string]any{}}); err != nil {
				return err
			}
		case "tools/list":
			if notification {
				continue
			}
			if !initialized {
				if err := writeError(request.ID, -32002, "server not initialized"); err != nil {
					return err
				}
				continue
			}
			if len(request.Params) > 0 && strings.TrimSpace(string(request.Params)) != "{}" && strings.TrimSpace(string(request.Params)) != "null" {
				var params struct {
					Cursor *string `json:"cursor"`
				}
				if err := json.Unmarshal(request.Params, &params); err != nil || params.Cursor != nil && strings.TrimSpace(*params.Cursor) != "" {
					if err := writeError(request.ID, -32602, "tools/list cursor is not supported"); err != nil {
						return err
					}
					continue
				}
			}
			if err := write(issueToolRPCResponse{ID: request.ID, Result: map[string]any{"tools": issueToolDefinitions()}}); err != nil {
				return err
			}
		case "tools/call":
			if notification {
				continue
			}
			if !initialized {
				if err := writeError(request.ID, -32002, "server not initialized"); err != nil {
					return err
				}
				continue
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
				if writeErr := writeError(request.ID, -32602, "tools/call requires name and object arguments"); writeErr != nil {
					return writeErr
				}
				continue
			}
			if len(params.Arguments) == 0 || strings.TrimSpace(string(params.Arguments)) == "null" {
				params.Arguments = json.RawMessage(`{}`)
			}
			var arguments map[string]json.RawMessage
			if err := json.Unmarshal(params.Arguments, &arguments); err != nil {
				if writeErr := writeError(request.ID, -32602, "tools/call arguments must be an object"); writeErr != nil {
					return writeErr
				}
				continue
			}
			known := false
			for _, definition := range issueToolDefinitions() {
				if definition.Name == params.Name {
					known = true
					break
				}
			}
			if !known {
				if writeErr := writeError(request.ID, -32601, "unknown tool"); writeErr != nil {
					return writeErr
				}
				continue
			}
			if forward == nil {
				if writeErr := writeError(request.ID, -32603, "issue tool forwarder is unavailable"); writeErr != nil {
					return writeErr
				}
				continue
			}
			forwardCtx, cancel := context.WithTimeout(ctx, options.ForwardTimeout)
			response, forwardErr := forward(forwardCtx, issueToolForwardRequest{Tool: params.Name, Arguments: params.Arguments})
			cancel()
			if forwardErr != nil {
				fmt.Fprintf(options.Logger, "issue tool forward failed: %v\n", forwardErr)
				// IPC failure does not prove the application action failed. Return
				// an explicit unknown tool result; never retry or claim not-dispatched.
				payload, _ := json.Marshal(map[string]string{
					"code": "operation_unknown", "message": "application bridge did not return a response; no automatic retry",
				})
				response = issueToolForwardResponse{
					Content: []issueToolContent{{Type: "text", Text: string(payload)}},
					IsError: true,
				}
			}
			if len(response.Content) == 0 {
				payload, _ := json.Marshal(map[string]string{"code": "invalid_forward_result", "message": "application response had no content"})
				response = issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: string(payload)}}, IsError: true}
			}
			if err := write(issueToolRPCResponse{ID: request.ID, Result: response}); err == errIssueToolResponseTooLarge {
				fallback := issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: `{"code":"response_too_large","message":"tool response exceeded the 4 MiB frame; no retry"}`}}, IsError: true}
				if fallbackErr := write(issueToolRPCResponse{ID: request.ID, Result: fallback}); fallbackErr != nil {
					return fallbackErr
				}
			} else if err != nil {
				return err
			}
		default:
			if notification {
				continue
			}
			if err := writeError(request.ID, -32601, "method not found"); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("issue tool helper input: %w", err)
	}
	return nil
}
