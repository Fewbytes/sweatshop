package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

func TestMCPInitializeAndToolList(t *testing.T) {
	paths, err := workspace.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d", len(messages))
	}
	if !strings.Contains(output.String(), `"name":"Bash"`) {
		t.Fatal("Bash tool missing")
	}
}
