package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

type Server struct{ Client rpc.Client }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type result struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func New(paths workspace.Paths) *Server { return &Server{Client: rpc.Client{Socket: paths.Socket}} }

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var request message
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		response := s.handle(ctx, request)
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, request message) result {
	switch request.Method {
	case "initialize":
		return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "agentsh", "version": "0.1.0"}}}
	case "tools/list":
		return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": toolDefinitions()}}
	case "tools/call":
		return s.call(ctx, request)
	default:
		return result{JSONRPC: "2.0", ID: request.ID, Error: map[string]any{"code": -32601, "message": "method not found"}}
	}
}

func toolDefinitions() []tool {
	stringSchema := func(required ...string) map[string]any {
		props := map[string]any{}
		for _, name := range required {
			props[name] = map[string]string{"type": "string"}
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	return []tool{
		{Name: "Bash", Description: "Run a bash command through agentsh.", InputSchema: stringSchema("command")},
		{Name: "BashOutput", Description: "Retrieve stored command output, optionally by lines or grep.", InputSchema: stringSchema("id")},
		{Name: "BashProcesses", Description: "List live agentsh invocations.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"session": map[string]string{"type": "string"}}}},
		{Name: "BashKill", Description: "Terminate an invocation.", InputSchema: stringSchema("id")},
		{Name: "BashInput", Description: "Write input to an interactive invocation.", InputSchema: stringSchema("id", "data")},
		{Name: "BashHistory", Description: "Query durable invocation history.", InputSchema: map[string]any{"type": "object"}},
		{Name: "BashState", Description: "Get session shell state.", InputSchema: map[string]any{"type": "object"}},
		{Name: "BashReplay", Description: "Replay a recorded invocation.", InputSchema: stringSchema("id")},
	}
}

func (s *Server) call(ctx context.Context, request message) result {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return result{JSONRPC: "2.0", ID: request.ID, Error: map[string]string{"message": err.Error()}}
	}
	var op string
	switch params.Name {
	case "Bash":
		op = rpc.OpBash
	case "BashOutput":
		op = rpc.OpBashOutput
	case "BashProcesses":
		op = rpc.OpBashProcesses
	case "BashKill":
		op = rpc.OpBashKill
	case "BashInput":
		op = rpc.OpBashInput
	case "BashHistory":
		op = rpc.OpBashHistory
	case "BashState":
		op = rpc.OpBashState
	case "BashReplay":
		op = rpc.OpBashReplay
	default:
		return result{JSONRPC: "2.0", ID: request.ID, Error: map[string]string{"message": "unknown tool"}}
	}
	data, _ := json.Marshal(params.Arguments)
	var value any
	if err := s.Client.Call(ctx, rpc.Request{ID: "mcp", Op: op, Params: data}, &value); err != nil {
		return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": fmt.Sprintf("%v", err)}}}}
	}
	encoded, _ := json.Marshal(value)
	return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"content": []any{map[string]string{"type": "text", "text": string(encoded)}}}}
}
