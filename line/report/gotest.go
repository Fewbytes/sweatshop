package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"time"
)

// ParseGoTestJSON extracts per-test results from a check log containing
// `go test -json` events. Lines that are not test events (lint output, build
// logs, a torn first line from a truncated log) are ignored, as are
// package-level events: a package that fails to build yields no test results.
func ParseGoTestJSON(log []byte) []TestResult {
	var out []TestResult
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(nil, 1<<22)
	for sc.Scan() {
		var ev struct {
			Action  string
			Package string
			Test    string
			Elapsed float64
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Test == "" {
			continue
		}
		outcome := TestOutcome(ev.Action)
		if outcome != TestPassed && outcome != TestFailed && outcome != TestSkipped {
			continue
		}
		out = append(out, TestResult{
			Package: ev.Package, Name: ev.Test, Outcome: outcome,
			Duration: time.Duration(ev.Elapsed * float64(time.Second)),
		})
	}
	return out
}
