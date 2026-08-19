package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
)

// requestTypes maps each tool to the daemon request struct it is marshalled
// into. A schema that omits a field makes that field unreachable for clients.
var requestTypes = map[string]any{
	"Bash":          rpc.BashRequest{},
	"BashOutput":    rpc.BashOutputRequest{},
	"BashProcesses": rpc.BashStateRequest{},
	"BashKill":      rpc.BashKillRequest{},
	"BashInput":     rpc.BashInputRequest{},
	"BashHistory":   rpc.BashHistoryRequest{},
	"BashState":     rpc.BashStateRequest{},
	"BashReplay":    rpc.BashReplayRequest{},
	"BashTemplates": rpc.BashTemplatesRequest{},
}

func schemaProperties(t *testing.T, def toolDef) map[string]any {
	t.Helper()
	props, ok := def.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s: schema has no properties object", def.Name)
	}
	return props
}

func TestEveryRequestFieldIsAdvertised(t *testing.T) {
	for _, def := range tools {
		request, ok := requestTypes[def.Name]
		if !ok {
			t.Fatalf("tool %s has no request type mapping", def.Name)
		}
		props := schemaProperties(t, def)
		value := reflect.TypeOf(request)
		for i := 0; i < value.NumField(); i++ {
			name := strings.Split(value.Field(i).Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			if _, found := props[name]; !found {
				t.Errorf("%s: schema omits %q, so clients cannot send it", def.Name, name)
			}
		}
	}
}

func TestSchemaTypesMatchRequestFields(t *testing.T) {
	expected := map[reflect.Kind]string{
		reflect.String: "string",
		reflect.Bool:   "boolean",
		reflect.Int:    "integer",
		reflect.Int64:  "integer",
	}
	for _, def := range tools {
		value := reflect.TypeOf(requestTypes[def.Name])
		props := schemaProperties(t, def)
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			prop, ok := props[name].(map[string]any)
			if !ok {
				continue
			}
			kind := field.Type.Kind()
			if kind == reflect.Ptr {
				kind = field.Type.Elem().Kind()
			}
			want, known := expected[kind]
			if !known {
				continue
			}
			if got := prop["type"]; got != want {
				t.Errorf("%s.%s: schema type %v, struct kind %v (want %q)", def.Name, name, got, kind, want)
			}
		}
	}
}

// Only fields without omitempty are genuinely required. Marking optional fields
// required rejects ordinary calls.
func TestRequiredFieldsAreActuallyRequired(t *testing.T) {
	for _, def := range tools {
		required, _ := def.Schema["required"].([]string)
		value := reflect.TypeOf(requestTypes[def.Name])
		optional := map[string]bool{}
		for i := 0; i < value.NumField(); i++ {
			parts := strings.Split(value.Field(i).Tag.Get("json"), ",")
			for _, option := range parts[1:] {
				if option == "omitempty" {
					optional[parts[0]] = true
				}
			}
		}
		for _, name := range required {
			if optional[name] {
				t.Errorf("%s: %q is marked required but is optional in %T", def.Name, name, requestTypes[def.Name])
			}
		}
	}
}

func TestEveryToolMapsToKnownOp(t *testing.T) {
	ops := map[string]bool{
		rpc.OpBash: true, rpc.OpBashOutput: true, rpc.OpBashProcesses: true,
		rpc.OpBashKill: true, rpc.OpBashInput: true, rpc.OpBashHistory: true,
		rpc.OpBashState: true, rpc.OpBashReplay: true, rpc.OpBashTemplates: true,
	}
	for _, def := range tools {
		if !ops[def.Op] {
			t.Errorf("%s maps to unknown op %q", def.Name, def.Op)
		}
		if _, found := toolsByName[def.Name]; !found {
			t.Errorf("%s missing from dispatch index", def.Name)
		}
	}
	if len(toolsByName) != len(tools) {
		t.Errorf("dispatch index has %d entries, tool list has %d", len(toolsByName), len(tools))
	}
}

// A command's own timeout must drive the client deadline. Inheriting a short
// default is what orphans long invocations.
func TestCallTimeoutFollowsRequestedCommandTimeout(t *testing.T) {
	bash := toolsByName["Bash"]
	if got := callTimeout(bash, map[string]any{"timeout_ms": float64(600000)}); got != 10*time.Minute+callGrace {
		t.Errorf("explicit timeout: got %v", got)
	}
	if got := callTimeout(bash, nil); got != defaultCommandTimeout+callGrace {
		t.Errorf("default timeout: got %v", got)
	}
	if got := callTimeout(bash, map[string]any{"timeout_ms": float64(0)}); got != defaultCommandTimeout+callGrace {
		t.Errorf("zero timeout should fall back to the default: got %v", got)
	}
	if got := callTimeout(toolsByName["BashState"], nil); got != controlCallTimeout {
		t.Errorf("control op: got %v", got)
	}
	if callTimeout(bash, nil) <= rpc.DefaultDialTimeout {
		t.Error("command deadline must exceed the dial timeout")
	}
}

func TestToolsListAdvertisesEverySchema(t *testing.T) {
	for _, listed := range toolDefinitions() {
		if listed.InputSchema == nil {
			t.Errorf("%s advertised without a schema", listed.Name)
		}
		if _, err := json.Marshal(listed.InputSchema); err != nil {
			t.Errorf("%s schema does not marshal: %v", listed.Name, err)
		}
	}
	if len(toolDefinitions()) != len(tools) {
		t.Error("tools/list does not advertise every tool")
	}
}
