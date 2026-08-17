package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func decodeLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("undecodable response %q: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	paths, err := workspace.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(paths)
}

// A message beyond the default 64KB scanner limit must still be handled; large
// stdin payloads are ordinary traffic.
func TestServeHandlesMessageLargerThanDefaultScannerBuffer(t *testing.T) {
	payload := strings.Repeat("x", 256*1024)
	request, err := json.Marshal(message{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"BashInput","arguments":{"id":"inv_1","data":"` + payload + `"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	// No daemon is listening, so the call fails — the point is that the
	// transport reads the message at all rather than dying on its length.
	if err := newTestServer(t).Serve(context.Background(), strings.NewReader(string(request)+"\n"), &output); err != nil {
		t.Fatalf("Serve returned %v on an oversized but valid message", err)
	}
	messages := decodeLines(t, output.String())
	if len(messages) != 1 {
		t.Fatalf("got %d responses, want 1", len(messages))
	}
}

// A malformed line owes the client a parse error; silence leaves it waiting.
func TestServeReportsParseErrorAndContinues(t *testing.T) {
	input := "{not json\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var output strings.Builder
	if err := newTestServer(t).Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeLines(t, output.String())
	if len(messages) != 2 {
		t.Fatalf("got %d responses, want 2 (parse error, then tools/list)", len(messages))
	}
	failure, ok := messages[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("first response carries no error object: %v", messages[0])
	}
	if code, _ := failure["code"].(float64); int(code) != codeParseError {
		t.Errorf("parse error code = %v, want %d", failure["code"], codeParseError)
	}
	if _, ok := messages[1]["result"]; !ok {
		t.Error("server did not continue after a malformed line")
	}
}

func TestUnknownToolReturnsMethodNotFoundWithCode(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"Nope","arguments":{}}}` + "\n"
	var output strings.Builder
	if err := newTestServer(t).Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeLines(t, output.String())
	failure, ok := messages[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object: %v", messages[0])
	}
	if code, _ := failure["code"].(float64); int(code) != codeMethodNotFound {
		t.Errorf("code = %v, want %d", failure["code"], codeMethodNotFound)
	}
}

func TestUnknownMethodCarriesErrorCode(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":4,"method":"resources/list"}` + "\n"
	var output strings.Builder
	if err := newTestServer(t).Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	failure, ok := decodeLines(t, output.String())[0]["error"].(map[string]any)
	if !ok {
		t.Fatal("no error object")
	}
	if _, present := failure["code"]; !present {
		t.Error("error object has no code field")
	}
}
