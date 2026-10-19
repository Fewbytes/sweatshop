package station

import (
	"encoding/json"
	"fmt"
)

// ClaudeRun is what a station's `claude -p --output-format json` run printed:
// the agent's final text and the tokens the run spent.
type ClaudeRun struct {
	Result string
	Tokens int64
}

// ParseClaudeRun reads the single JSON result object of a finished claude -p
// run. Tokens counts input, output and cache-creation tokens. Cache reads are
// left out: they re-read the same context on every turn, so counting them
// would meter a long run many times over for one piece of context.
func ParseClaudeRun(out []byte) (ClaudeRun, error) {
	var raw struct {
		Result string `json:"result"`
		Usage  *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return ClaudeRun{}, fmt.Errorf("parse claude result: %w", err)
	}
	if raw.Usage == nil {
		return ClaudeRun{}, fmt.Errorf("claude result has no usage")
	}
	u := raw.Usage
	return ClaudeRun{Result: raw.Result, Tokens: u.InputTokens + u.OutputTokens + u.CacheCreationTokens}, nil
}
