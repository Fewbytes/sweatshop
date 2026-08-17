package session

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
)

type State struct {
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Functions string            `json:"functions"`
	Options   string            `json:"options"`
	Shopt     string            `json:"shopt"`
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]State
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]State),
	}
}

func (m *Manager) Get(name string, defaultCWD string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.sessions[name]; ok {
		return state
	}
	return State{
		CWD: defaultCWD,
		Env: make(map[string]string),
	}
}

func (m *Manager) Set(name string, state State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[name] = state
}

// BuildPrelude generates the shell script fragment to restore the session state
func (s State) BuildPrelude() string {
	var buf strings.Builder
	if s.CWD != "" {
		fmt.Fprintf(&buf, "cd %q 2>/dev/null || true\n", s.CWD)
	}
	if s.Options != "" {
		buf.WriteString(s.Options)
		buf.WriteByte('\n')
	}
	if s.Shopt != "" {
		buf.WriteString(s.Shopt)
		buf.WriteByte('\n')
	}
	if s.Functions != "" {
		buf.WriteString(s.Functions)
		buf.WriteByte('\n')
	}
	for k, v := range s.Env {
		fmt.Fprintf(&buf, "export %s=%q\n", k, v)
	}
	return buf.String()
}

// StateCaptureScript returns the post-command shell snippet that writes state to FD 3
func StateCaptureScript() string {
	return `
__agentsh_code=$?
{
  printf '===PWD===\n'
  pwd
  printf '===ENV===\n'
  export -p
  printf '===FUNCS===\n'
  declare -f
  printf '===OPTS===\n'
  set +o
  printf '===SHOPT===\n'
  shopt -p
  printf '===END===\n'
} >&3 2>/dev/null || true
(exit $__agentsh_code)
`
}

// ParseCapturedState parses the text emitted to FD 3 into a State struct
func ParseCapturedState(data []byte, baselineState State) (State, map[string]string) {
	state := State{
		CWD: baselineState.CWD,
		Env: make(map[string]string),
	}
	envDelta := make(map[string]string)

	parts := bytes.Split(data, []byte("\n==="))
	for _, part := range parts {
		str := string(part)
		if !strings.HasPrefix(str, "===") && strings.Contains(str, "===\n") {
			str = "===" + str
		}
		if strings.HasPrefix(str, "===PWD===\n") {
			state.CWD = strings.TrimSpace(strings.TrimPrefix(str, "===PWD===\n"))
		} else if strings.HasPrefix(str, "===ENV===\n") {
			lines := strings.Split(strings.TrimPrefix(str, "===ENV===\n"), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "declare -x ") || strings.HasPrefix(line, "export ") {
					line = strings.TrimPrefix(line, "declare -x ")
					line = strings.TrimPrefix(line, "export ")
					if k, v, ok := strings.Cut(line, "="); ok {
						v = strings.Trim(v, "\"")
						state.Env[k] = v
						if baselineState.Env[k] != v {
							envDelta[k] = v
						}
					}
				}
			}
		} else if strings.HasPrefix(str, "===FUNCS===\n") {
			state.Functions = strings.TrimSpace(strings.TrimPrefix(str, "===FUNCS===\n"))
		} else if strings.HasPrefix(str, "===OPTS===\n") {
			state.Options = strings.TrimSpace(strings.TrimPrefix(str, "===OPTS===\n"))
		} else if strings.HasPrefix(str, "===SHOPT===\n") {
			state.Shopt = strings.TrimSpace(strings.TrimPrefix(str, "===SHOPT===\n"))
		}
	}

	return state, envDelta
}
