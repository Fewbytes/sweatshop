package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/version"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

const protocolVersion = "2024-11-05"

// maxMessageBytes bounds a single JSON-RPC line. The default scanner limit of
// 64KB is far too small: any tool call carrying a large stdin payload exceeds
// it, and the scanner then reports an error the loop cannot recover from.
const maxMessageBytes = 16 << 20

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidParams  = -32602
	codeMethodNotFound = -32601
)

const (
	// defaultCommandTimeout mirrors executor.DefaultTimeout. Duplicated rather
	// than imported to keep the transport free of executor dependencies; the
	// daemon remains the authority, this only sizes the client's patience.
	defaultCommandTimeout = 120 * time.Second

	// callGrace is added to a command's own timeout so the daemon always gets
	// the chance to report a timeout itself rather than having the client give
	// up first and orphan the invocation.
	callGrace = 30 * time.Second

	// controlCallTimeout bounds calls that only touch daemon state.
	controlCallTimeout = 30 * time.Second
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

// toolDef binds a tool's advertised schema to the daemon operation it invokes,
// so the tool list and the dispatch path cannot drift apart.
type toolDef struct {
	Name        string
	Op          string
	Description string
	Schema      map[string]any
}

func New(paths workspace.Paths) *Server { return &Server{Client: rpc.Client{Socket: paths.Socket}} }

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageBytes)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var request message
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			// A malformed line still owes the client a response; staying silent
			// leaves it waiting forever on a request it believes is in flight.
			if err := encoder.Encode(errorResult(nil, codeParseError, err.Error())); err != nil {
				return err
			}
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
		return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "agentsh", "version": version.Version},
		}}
	case "tools/list":
		return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": toolDefinitions()}}
	case "tools/call":
		return s.call(ctx, request)
	default:
		return errorResult(request.ID, codeMethodNotFound, "method not found")
	}
}

func errorResult(id json.RawMessage, code int, message string) result {
	return result{JSONRPC: "2.0", ID: id, Error: map[string]any{"code": code, "message": message}}
}

// Schema helpers. Optional fields must stay out of "required"; marking
// everything required makes ordinary calls invalid.
func object(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumProp(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// tools is the single source of truth for both tools/list and dispatch. Field
// names and types mirror the rpc.*Request structs.
var tools = []toolDef{
	{
		Name: "Bash", Op: rpc.OpBash,
		Description: "Run a bash command through agentsh. Output is stored outside context; retrieve it with BashOutput.",
		Schema: object(map[string]any{
			"command":      stringProp("Command to run."),
			"session":      stringProp("Session name carrying cwd and environment between commands. Defaults to \"default\"."),
			"timeout_ms":   intProp("Kill the command after this many milliseconds. Defaults to 120000."),
			"idle_wait_ms": intProp("Report the command as waiting on input after this long with no output."),
			"background":   boolProp("Return immediately with an invocation id instead of waiting."),
			"interactive":  boolProp("Keep stdin open so BashInput can write to the command."),
			"stdin":        stringProp("Data to write to the command's stdin."),
		}, "command"),
	},
	{
		Name: "BashOutput", Op: rpc.OpBashOutput,
		Description: "Retrieve stored command output, optionally by line range or grep.",
		Schema: object(map[string]any{
			"id":      stringProp("Invocation id."),
			"stream":  enumProp("Which stream to read. Defaults to stdout.", "stdout", "stderr"),
			"lines":   stringProp("Line range, e.g. \"1-100\" or \"-50\" for the last 50."),
			"grep":    stringProp("Return only matching lines."),
			"context": intProp("Lines of context to include around each grep match."),
			"mode":    enumProp("Set to \"templates\" for clustered log templates instead of raw text.", "text", "templates"),
		}, "id"),
	},
	{
		Name: "BashProcesses", Op: rpc.OpBashProcesses,
		Description: "List live agentsh invocations.",
		Schema: object(map[string]any{
			"session": stringProp("Restrict to one session."),
		}),
	},
	{
		Name: "BashKill", Op: rpc.OpBashKill,
		Description: "Terminate an invocation and its process tree.",
		Schema: object(map[string]any{
			"id":     stringProp("Invocation id."),
			"signal": intProp("Signal number. Defaults to SIGTERM (15)."),
		}, "id"),
	},
	{
		Name: "BashInput", Op: rpc.OpBashInput,
		Description: "Write input to an interactive invocation.",
		Schema: object(map[string]any{
			"id":   stringProp("Invocation id."),
			"data": stringProp("Data to write to stdin."),
		}, "id", "data"),
	},
	{
		Name: "BashHistory", Op: rpc.OpBashHistory,
		Description: "Query durable invocation history.",
		Schema: object(map[string]any{
			"session": stringProp("Restrict to one session."),
			"command": stringProp("Full-text match against recorded commands."),
			"exit":    intProp("Restrict to invocations with this exit code."),
			"since":   stringProp("RFC3339 timestamp; only invocations started at or after it."),
			"limit":   intProp("Maximum rows to return. Defaults to 100."),
		}),
	},
	{
		Name: "BashState", Op: rpc.OpBashState,
		Description: "Get session shell state: cwd, environment, functions, shell options.",
		Schema: object(map[string]any{
			"session": stringProp("Session name. Defaults to \"default\"."),
		}),
	},
	{
		Name: "BashReplay", Op: rpc.OpBashReplay,
		Description: "Replay a recorded invocation verbatim.",
		Schema: object(map[string]any{
			"id": stringProp("Invocation id to replay."),
		}, "id"),
	},
	{
		Name: "BashTemplates", Op: rpc.OpBashTemplates,
		Description: "Retrieve clustered log templates, stack traces, level histogram, and baseline diff for an invocation stream.",
		Schema: object(map[string]any{
			"id":       stringProp("Invocation id."),
			"stream":   enumProp("Which stream to analyse. Defaults to stdout.", "stdout", "stderr"),
			"baseline": boolProp("Compare against prior runs of the same command."),
		}, "id"),
	},
}

var toolsByName = func() map[string]toolDef {
	index := make(map[string]toolDef, len(tools))
	for _, def := range tools {
		index[def.Name] = def
	}
	return index
}()

func toolDefinitions() []tool {
	list := make([]tool, 0, len(tools))
	for _, def := range tools {
		list = append(list, tool{Name: def.Name, Description: def.Description, InputSchema: def.Schema})
	}
	return list
}

func (s *Server) call(ctx context.Context, request message) result {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return errorResult(request.ID, codeInvalidParams, err.Error())
	}
	def, ok := toolsByName[params.Name]
	if !ok {
		return errorResult(request.ID, codeMethodNotFound, "unknown tool: "+params.Name)
	}
	data, err := json.Marshal(params.Arguments)
	if err != nil {
		return errorResult(request.ID, codeInvalidParams, err.Error())
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout(def, params.Arguments))
	defer cancel()

	var value any
	if err := s.Client.Call(callCtx, rpc.Request{ID: "mcp", Op: def.Op, Params: data}, &value); err != nil {
		return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
			"isError": true,
			"content": []any{map[string]string{"type": "text", "text": fmt.Sprintf("%v", err)}},
		}}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return errorResult(request.ID, codeParseError, err.Error())
	}
	return result{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
		"content": []any{map[string]string{"type": "text", "text": string(encoded)}},
	}}
}

// callTimeout keeps the client's patience ahead of the daemon's own deadline.
// Commands may run for as long as the caller asked; control operations answer
// from daemon state and should not hang.
func callTimeout(def toolDef, args map[string]any) time.Duration {
	if def.Op != rpc.OpBash && def.Op != rpc.OpBashReplay {
		return controlCallTimeout
	}
	command := defaultCommandTimeout
	if ms, ok := args["timeout_ms"].(float64); ok && ms > 0 {
		command = time.Duration(ms) * time.Millisecond
	}
	return command + callGrace
}
