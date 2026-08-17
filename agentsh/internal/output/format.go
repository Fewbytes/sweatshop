package output

import (
	"bufio"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

const (
	MaxSummaryFailures    = 20
	MaxFailureExcerptLine = 10
)

// DetectFamily conservatively identifies if a command belongs to one of the
// supported structured command families:
//   - "go test"
//   - "pytest"
//   - "jest"
//   - "cargo"
//   - "kubectl"
//   - "terraform"
func DetectFamily(command string) (string, bool) {
	cmd := cleanCommandLine(command)
	if cmd == "" {
		return "", false
	}
	tokens := tokenizeCommand(cmd)
	if len(tokens) == 0 {
		return "", false
	}

	exe := filepath.Base(tokens[0])
	args := tokens[1:]

	// 1. go test
	if exe == "go" || exe == "go.exe" {
		if len(args) > 0 && args[0] == "test" {
			return "go test", true
		}
		return "", false
	}

	// 2. pytest
	if exe == "pytest" || strings.HasPrefix(exe, "pytest-") {
		return "pytest", true
	}
	if strings.HasPrefix(exe, "python") {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-m" && (args[i+1] == "pytest" || strings.HasPrefix(args[i+1], "pytest")) {
				return "pytest", true
			}
		}
	}
	if exe == "poetry" || exe == "pipenv" {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "run" && (args[i+1] == "pytest" || strings.HasPrefix(args[i+1], "pytest")) {
				return "pytest", true
			}
		}
	}

	// 3. jest
	if exe == "jest" {
		return "jest", true
	}
	if exe == "npx" || exe == "yarn" || exe == "pnpm" || exe == "bun" {
		for _, arg := range args {
			if arg == "jest" {
				return "jest", true
			}
		}
	}
	if (exe == "npm" || exe == "yarn" || exe == "pnpm" || exe == "bun") && len(args) > 0 && args[0] == "test" {
		return "jest", true
	}

	// 4. cargo
	if exe == "cargo" || exe == "cargo.exe" {
		return "cargo", true
	}

	// 5. kubectl
	if exe == "kubectl" || exe == "kubectl.exe" {
		return "kubectl", true
	}

	// 6. terraform
	if exe == "terraform" || exe == "terraform.exe" || exe == "tofu" || exe == "tofu.exe" {
		return "terraform", true
	}

	return "", false
}

// FormatCommand inspects the command and its output to derive a compact,
// structured summary. Returns nil if the command is not a supported family.
func FormatCommand(command, stdout, stderr string, exitCode int) *storage.CommandSummary {
	family, ok := DetectFamily(command)
	if !ok {
		return nil
	}

	switch family {
	case "go test":
		return formatGoTest(command, stdout, stderr, exitCode)
	case "pytest":
		return formatPytest(command, stdout, stderr, exitCode)
	case "jest":
		return formatJest(command, stdout, stderr, exitCode)
	case "cargo":
		return formatCargo(command, stdout, stderr, exitCode)
	case "kubectl":
		return formatKubectl(command, stdout, stderr, exitCode)
	case "terraform":
		return formatTerraform(command, stdout, stderr, exitCode)
	default:
		return nil
	}
}

// cleanCommandLine strips leading environment assignments and takes the last
// command segment if chained with && or ;.
func cleanCommandLine(command string) string {
	s := strings.TrimSpace(command)
	if s == "" {
		return ""
	}

	// If chained by && or ;, extract the final relevant command.
	if idx := strings.LastIndex(s, "&&"); idx != -1 {
		s = strings.TrimSpace(s[idx+2:])
	}
	if idx := strings.LastIndex(s, ";"); idx != -1 {
		s = strings.TrimSpace(s[idx+1:])
	}

	// Strip leading env var definitions: FOO=bar, export FOO=bar
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "export ") {
			s = strings.TrimSpace(strings.TrimPrefix(s, "export "))
		}
		if eqIdx := strings.Index(s, "="); eqIdx > 0 {
			firstWord := s[:eqIdx]
			if isIdent(firstWord) {
				// Skip past the value
				rem := s[eqIdx+1:]
				if len(rem) > 0 && (rem[0] == '"' || rem[0] == '\'') {
					quote := rem[0]
					if endQ := strings.IndexByte(rem[1:], quote); endQ != -1 {
						s = rem[endQ+2:]
						continue
					}
				}
				if spaceIdx := strings.IndexAny(rem, " \t"); spaceIdx != -1 {
					s = rem[spaceIdx+1:]
					continue
				}
				return ""
			}
		}
		break
	}
	return strings.TrimSpace(s)
}

func isIdent(s string) bool {
	for i, r := range s {
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
			return false
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return len(s) > 0
}

func tokenizeCommand(cmd string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for _, r := range cmd {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && !inSingle {
			escaped = true
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// --- 1. go test ---

var (
	goTestPassRe = regexp.MustCompile(`^--- PASS:\s+(\S+)`)
	goTestFailRe = regexp.MustCompile(`^--- FAIL:\s+(\S+)`)
	goTestSkipRe = regexp.MustCompile(`^--- SKIP:\s+(\S+)`)
	goTestRunRe  = regexp.MustCompile(`^=== RUN\s+(\S+)`)
	goPkgOkRe    = regexp.MustCompile(`^ok\s+(\S+)\s+([0-9.]+[a-z]+)`)
	goPkgFailRe  = regexp.MustCompile(`^FAIL\s+(\S+)\s+([0-9.]+[a-z]+)`)
	goPkgSkipRe  = regexp.MustCompile(`^\?\s+(\S+)\s+\[no test files\]`)
	goLocRe      = regexp.MustCompile(`^\s*([a-zA-Z0-9_./\\-]+\.go:\d+):\s*(.*)$`)
	goCompileRe  = regexp.MustCompile(`^([a-zA-Z0-9_./\\-]+\.go:\d+(?::\d+)?):\s*(.+)$`)
)

func formatGoTest(_ string, stdout, stderr string, exitCode int) *storage.CommandSummary {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	s := &storage.CommandSummary{
		Family: "go test",
		Status: "passed",
	}
	if exitCode != 0 {
		s.Status = "failed"
	}

	lines := scanLines(combined)

	var failures []storage.Failure
	var runBuffer []string
	var currentTestName string

	testsPassed, testsFailed, testsSkipped := 0, 0, 0
	pkgsPassed, pkgsFailed, pkgsSkipped := 0, 0, 0
	var totalDuration string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for === RUN
		if m := goTestRunRe.FindStringSubmatch(trimmed); m != nil {
			currentTestName = m[1]
			runBuffer = nil
			continue
		}

		// Check test outcomes
		if m := goTestPassRe.FindStringSubmatch(trimmed); m != nil {
			testsPassed++
			currentTestName = ""
			runBuffer = nil
			continue
		}
		if m := goTestSkipRe.FindStringSubmatch(trimmed); m != nil {
			testsSkipped++
			currentTestName = ""
			runBuffer = nil
			continue
		}
		if m := goTestFailRe.FindStringSubmatch(trimmed); m != nil {
			testsFailed++
			failName := m[1]
			if failName == "" {
				failName = currentTestName
			}
			if len(failures) < MaxSummaryFailures {
				f := storage.Failure{Name: failName}
				for _, rLine := range runBuffer {
					if locM := goLocRe.FindStringSubmatch(rLine); locM != nil {
						if f.Location == "" {
							f.Location = locM[1]
						}
						if f.Message == "" {
							f.Message = locM[2]
						}
					} else if f.Message == "" && strings.TrimSpace(rLine) != "" && !strings.HasPrefix(strings.TrimSpace(rLine), "---") {
						f.Message = strings.TrimSpace(rLine)
					}
				}
				if len(runBuffer) > 0 {
					f.Excerpt = boundExcerpt(runBuffer)
				}
				failures = append(failures, f)
			}
			currentTestName = ""
			runBuffer = nil
			continue
		}

		// Package outcome lines
		if m := goPkgOkRe.FindStringSubmatch(trimmed); m != nil {
			pkgsPassed++
			totalDuration = m[2]
			continue
		}
		if m := goPkgFailRe.FindStringSubmatch(trimmed); m != nil {
			pkgsFailed++
			totalDuration = m[2]
			continue
		}
		if m := goPkgSkipRe.FindStringSubmatch(trimmed); m != nil {
			pkgsSkipped++
			continue
		}

		// Build failure / compilation error in go test
		if strings.HasSuffix(trimmed, "[build failed]") || (exitCode != 0 && strings.HasPrefix(trimmed, "# ")) {
			for j := i + 1; j < len(lines) && j < i+10; j++ {
				cLine := strings.TrimSpace(lines[j])
				if cm := goCompileRe.FindStringSubmatch(cLine); cm != nil {
					if len(failures) < MaxSummaryFailures {
						failures = append(failures, storage.Failure{
							Name:     "build failed",
							Location: cm[1],
							Message:  cm[2],
							Excerpt:  cLine,
						})
					}
				}
			}
			continue
		}

		if currentTestName != "" {
			if len(runBuffer) < MaxFailureExcerptLine {
				runBuffer = append(runBuffer, line)
			}
		}
	}

	if testsPassed+testsFailed+testsSkipped > 0 {
		s.Passed = testsPassed
		s.Failed = testsFailed
		s.Skipped = testsSkipped
		s.Total = testsPassed + testsFailed + testsSkipped
	} else if pkgsPassed+pkgsFailed+pkgsSkipped > 0 {
		s.Passed = pkgsPassed
		s.Failed = pkgsFailed
		s.Skipped = pkgsSkipped
		s.Total = pkgsPassed + pkgsFailed + pkgsSkipped
	} else if exitCode == 0 && (strings.Contains(combined, "PASS") || strings.Contains(combined, "ok")) {
		s.Passed = 1
		s.Total = 1
	} else if exitCode != 0 {
		s.Failed = 1
		s.Total = 1
	}

	if s.Failed > 0 || exitCode != 0 {
		s.Status = "failed"
	}
	s.Duration = totalDuration
	s.Failures = failures

	if s.Total > 0 {
		var parts []string
		if s.Passed > 0 {
			parts = append(parts, fmt.Sprintf("%d passed", s.Passed))
		}
		if s.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
		}
		if s.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped", s.Skipped))
		}
		s.Details = strings.Join(parts, ", ")
		if s.Duration != "" {
			s.Details += " in " + s.Duration
		}
	} else if exitCode == 0 {
		s.Details = "passed"
	} else {
		s.Details = "failed"
	}

	return s
}

// --- 2. pytest ---

var (
	pytestSummaryRe = regexp.MustCompile(`=+\s*(?:(\d+)\s+failed,?\s*)?(?:(\d+)\s+passed,?\s*)?(?:(\d+)\s+skipped,?\s*)?(?:(\d+)\s+xfailed,?\s*)?(?:(\d+)\s+xpassed,?\s*)?(?:(\d+)\s+error[s]?,?\s*)?in\s+([0-9.]+[a-z]+)`)
	pytestShortFail = regexp.MustCompile(`^FAILED\s+(\S+)(?:\s+-\s+(.+))?$`)
	pytestShortErr  = regexp.MustCompile(`^ERROR\s+(\S+)(?:\s+-\s+(.+))?$`)
	pytestLocRe     = regexp.MustCompile(`^([a-zA-Z0-9_./\\-]+\.py:\d+):\s*(.*)$`)
)

func formatPytest(_ string, stdout, stderr string, exitCode int) *storage.CommandSummary {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	s := &storage.CommandSummary{
		Family: "pytest",
		Status: "passed",
	}
	if exitCode != 0 {
		s.Status = "failed"
	}

	lines := scanLines(combined)

	// Summary line search (typically at the end)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if m := pytestSummaryRe.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				s.Failed, _ = strconv.Atoi(m[1])
			}
			if m[2] != "" {
				s.Passed, _ = strconv.Atoi(m[2])
			}
			if m[3] != "" {
				s.Skipped, _ = strconv.Atoi(m[3])
			}
			if m[4] != "" {
				xf, _ := strconv.Atoi(m[4])
				s.Skipped += xf
			}
			if m[5] != "" {
				xp, _ := strconv.Atoi(m[5])
				s.Passed += xp
			}
			if m[6] != "" {
				errCount, _ := strconv.Atoi(m[6])
				s.Failed += errCount
			}
			s.Duration = m[7]
			s.Total = s.Passed + s.Failed + s.Skipped
			break
		}
	}

	// Extract failure items
	var failures []storage.Failure
	inShortSummary := false
	inFailuresBlock := false
	var currentFail *storage.Failure
	var currentExcerpt []string

	finishFail := func() {
		if currentFail != nil {
			if len(currentExcerpt) > 0 {
				currentFail.Excerpt = boundExcerpt(currentExcerpt)
			}
			failures = append(failures, *currentFail)
			currentFail = nil
			currentExcerpt = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.Contains(trimmed, "short test summary info") {
			finishFail()
			inShortSummary = true
			inFailuresBlock = false
			continue
		}
		if strings.Contains(trimmed, "FAILURES") || strings.Contains(trimmed, "ERRORS") {
			finishFail()
			inFailuresBlock = true
			continue
		}
		if inShortSummary && strings.HasPrefix(trimmed, "===") {
			inShortSummary = false
			continue
		}

		if inShortSummary {
			if m := pytestShortFail.FindStringSubmatch(trimmed); m != nil {
				if len(failures) < MaxSummaryFailures {
					f := storage.Failure{Name: m[1]}
					if len(m) > 2 {
						f.Message = m[2]
					}
					if locM := pytestLocRe.FindStringSubmatch(m[1]); locM != nil {
						f.Location = locM[1]
					}
					failures = append(failures, f)
				}
				continue
			}
			if m := pytestShortErr.FindStringSubmatch(trimmed); m != nil {
				if len(failures) < MaxSummaryFailures {
					f := storage.Failure{Name: m[1]}
					if len(m) > 2 {
						f.Message = m[2]
					}
					failures = append(failures, f)
				}
				continue
			}
		}

		if inFailuresBlock && !inShortSummary {
			if strings.HasPrefix(trimmed, "___") && strings.HasSuffix(trimmed, "___") {
				finishFail()
				name := strings.Trim(trimmed, "_ ")
				if len(failures) < MaxSummaryFailures {
					currentFail = &storage.Failure{Name: name}
				}
				continue
			}
			if currentFail != nil {
				if strings.HasPrefix(trimmed, "===") {
					finishFail()
					inFailuresBlock = false
					continue
				}
				if len(currentExcerpt) < MaxFailureExcerptLine {
					currentExcerpt = append(currentExcerpt, line)
				}
				if strings.HasPrefix(trimmed, "E   ") || strings.HasPrefix(trimmed, "E   ") {
					msg := strings.TrimPrefix(trimmed, "E   ")
					if currentFail.Message == "" {
						currentFail.Message = msg
					}
				}
				if locM := pytestLocRe.FindStringSubmatch(trimmed); locM != nil && currentFail.Location == "" {
					currentFail.Location = locM[1]
				}
			}
		}
	}
	finishFail()

	s.Failures = failures
	if s.Failed > 0 || exitCode != 0 {
		s.Status = "failed"
	}

	if s.Total > 0 {
		var parts []string
		if s.Passed > 0 {
			parts = append(parts, fmt.Sprintf("%d passed", s.Passed))
		}
		if s.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
		}
		if s.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped", s.Skipped))
		}
		s.Details = strings.Join(parts, ", ")
		if s.Duration != "" {
			s.Details += " in " + s.Duration
		}
	} else if exitCode == 0 {
		s.Details = "passed"
	} else {
		s.Details = "failed"
	}

	return s
}

// --- 3. jest ---

var (
	jestTestsRe = regexp.MustCompile(`Tests:\s+(?:(\d+)\s+failed,\s*)?(?:(\d+)\s+skipped,\s*)?(?:(\d+)\s+passed,\s*)?(\d+)\s+total`)
	jestTimeRe  = regexp.MustCompile(`Time:\s+([0-9.]+\s*s)`)
	jestLocRe   = regexp.MustCompile(`at\s+(?:.*?\s+)?\(?([a-zA-Z0-9_./\\-]+\.[a-zA-Z0-9]+:\d+:\d+)\)?`)
)

func formatJest(_ string, stdout, stderr string, exitCode int) *storage.CommandSummary {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	s := &storage.CommandSummary{
		Family: "jest",
		Status: "passed",
	}
	if exitCode != 0 {
		s.Status = "failed"
	}

	lines := scanLines(combined)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := jestTestsRe.FindStringSubmatch(trimmed); m != nil {
			if m[1] != "" {
				s.Failed, _ = strconv.Atoi(m[1])
			}
			if m[2] != "" {
				s.Skipped, _ = strconv.Atoi(m[2])
			}
			if m[3] != "" {
				s.Passed, _ = strconv.Atoi(m[3])
			}
			if m[4] != "" {
				s.Total, _ = strconv.Atoi(m[4])
			}
		}
		if m := jestTimeRe.FindStringSubmatch(trimmed); m != nil {
			s.Duration = m[1]
		}
	}

	var failures []storage.Failure
	var currentFail *storage.Failure
	var currentExcerpt []string

	finishFail := func() {
		if currentFail != nil {
			if len(currentExcerpt) > 0 {
				currentFail.Excerpt = boundExcerpt(currentExcerpt)
			}
			failures = append(failures, *currentFail)
			currentFail = nil
			currentExcerpt = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "●") {
			finishFail()
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "●"))
			if len(failures) < MaxSummaryFailures {
				currentFail = &storage.Failure{Name: name}
			}
			continue
		}
		if currentFail != nil {
			if strings.HasPrefix(trimmed, "Test Suites:") || strings.HasPrefix(trimmed, "PASS ") || strings.HasPrefix(trimmed, "FAIL ") {
				finishFail()
				continue
			}
			if len(currentExcerpt) < MaxFailureExcerptLine {
				currentExcerpt = append(currentExcerpt, line)
			}
			if locM := jestLocRe.FindStringSubmatch(trimmed); locM != nil && currentFail.Location == "" {
				currentFail.Location = locM[1]
			}
			if strings.HasPrefix(trimmed, "Expected:") || strings.HasPrefix(trimmed, "Received:") || strings.HasPrefix(trimmed, "Error:") {
				if currentFail.Message == "" {
					currentFail.Message = trimmed
				} else {
					currentFail.Message += "; " + trimmed
				}
			}
		}
	}
	finishFail()

	s.Failures = failures
	if s.Failed > 0 || exitCode != 0 {
		s.Status = "failed"
	}

	if s.Total > 0 {
		var parts []string
		if s.Passed > 0 {
			parts = append(parts, fmt.Sprintf("%d passed", s.Passed))
		}
		if s.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
		}
		if s.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped", s.Skipped))
		}
		s.Details = strings.Join(parts, ", ")
		if s.Duration != "" {
			s.Details += " in " + s.Duration
		}
	} else if exitCode == 0 {
		s.Details = "passed"
	} else {
		s.Details = "failed"
	}

	return s
}

// --- 4. cargo ---

var (
	cargoTestResultRe = regexp.MustCompile(`test result:\s+(\w+)\.\s+(\d+)\s+passed;\s+(\d+)\s+failed;\s+(\d+)\s+ignored;.*finished in\s+([0-9.]+)s`)
	cargoPanicRe      = regexp.MustCompile(`panicked at\s+(\S+:\d+:\d+):\s*(.*)`)
	cargoCompileErrRe = regexp.MustCompile(`^error(?:\[E\d+\])?:\s*(.+)`)
	cargoLocRe        = regexp.MustCompile(`^\s*-->\s*(\S+:\d+:\d+)`)
)

func formatCargo(_ string, stdout, stderr string, exitCode int) *storage.CommandSummary {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	s := &storage.CommandSummary{
		Family: "cargo",
		Status: "ok",
	}
	if exitCode != 0 {
		s.Status = "failed"
	}

	lines := scanLines(combined)

	isTest := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := cargoTestResultRe.FindStringSubmatch(trimmed); m != nil {
			isTest = true
			s.Passed, _ = strconv.Atoi(m[2])
			s.Failed, _ = strconv.Atoi(m[3])
			s.Skipped, _ = strconv.Atoi(m[4])
			s.Total = s.Passed + s.Failed + s.Skipped
			s.Duration = m[5] + "s"
			if m[1] == "ok" {
				s.Status = "passed"
			} else {
				s.Status = "failed"
			}
			break
		}
	}

	var failures []storage.Failure

	if isTest {
		var currentFail *storage.Failure
		var currentExcerpt []string

		finishFail := func() {
			if currentFail != nil {
				if len(currentExcerpt) > 0 {
					currentFail.Excerpt = boundExcerpt(currentExcerpt)
				}
				failures = append(failures, *currentFail)
				currentFail = nil
				currentExcerpt = nil
			}
		}

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "---- ") && strings.HasSuffix(trimmed, " stdout ----") {
				finishFail()
				name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "---- "), " stdout ----")
				if len(failures) < MaxSummaryFailures {
					currentFail = &storage.Failure{Name: name}
				}
				continue
			}
			if currentFail != nil {
				if strings.HasPrefix(trimmed, "failures:") || strings.HasPrefix(trimmed, "test result:") {
					finishFail()
					continue
				}
				if len(currentExcerpt) < MaxFailureExcerptLine {
					currentExcerpt = append(currentExcerpt, line)
				}
				if pM := cargoPanicRe.FindStringSubmatch(trimmed); pM != nil {
					currentFail.Location = pM[1]
					if pM[2] != "" {
						currentFail.Message = pM[2]
					}
				} else if strings.HasPrefix(trimmed, "assertion `") || strings.HasPrefix(trimmed, "assertion failed") || strings.HasPrefix(trimmed, "assertion left == right") {
					if currentFail.Message == "" {
						currentFail.Message = trimmed
					}
				}
			}
		}
		finishFail()

		s.Failures = failures
		var parts []string
		if s.Passed > 0 {
			parts = append(parts, fmt.Sprintf("%d passed", s.Passed))
		}
		if s.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
		}
		if s.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d ignored", s.Skipped))
		}
		s.Details = strings.Join(parts, ", ")
		if s.Duration != "" {
			s.Details += " in " + s.Duration
		}
		return s
	}

	// Non-test cargo build / check / clippy
	var currentErr *storage.Failure
	var currentExcerpt []string

	finishErr := func() {
		if currentErr != nil {
			if len(currentExcerpt) > 0 {
				currentErr.Excerpt = boundExcerpt(currentExcerpt)
			}
			failures = append(failures, *currentErr)
			currentErr = nil
			currentExcerpt = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := cargoCompileErrRe.FindStringSubmatch(trimmed); m != nil {
			finishErr()
			if len(failures) < MaxSummaryFailures {
				currentErr = &storage.Failure{Message: trimmed}
			}
			continue
		}
		if currentErr != nil {
			if strings.HasPrefix(trimmed, "error: could not compile") || strings.HasPrefix(trimmed, "Finished ") {
				finishErr()
				continue
			}
			if len(currentExcerpt) < MaxFailureExcerptLine {
				currentExcerpt = append(currentExcerpt, line)
			}
			if locM := cargoLocRe.FindStringSubmatch(line); locM != nil && currentErr.Location == "" {
				currentErr.Location = locM[1]
			}
		}
	}
	finishErr()

	s.Failures = failures
	if len(failures) > 0 {
		s.Failed = len(failures)
		s.Status = "failed"
		s.Details = fmt.Sprintf("build failed (%d errors)", len(failures))
	} else if exitCode == 0 {
		s.Status = "ok"
		s.Details = "build finished successfully"
	} else {
		s.Status = "failed"
		s.Details = "build failed"
	}

	return s
}

// --- 5. kubectl ---

var (
	k8sActionRe = regexp.MustCompile(`^(\S+)\s+(created|configured|unchanged|deleted|server-side-applied|exposed)$`)
	k8sErrorRe  = regexp.MustCompile(`^(?:Error from server \(([^)]+)\)|error):\s*(.+)`)
)

func formatKubectl(_ string, stdout, stderr string, exitCode int) *storage.CommandSummary {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	s := &storage.CommandSummary{
		Family: "kubectl",
		Status: "ok",
	}
	if exitCode != 0 {
		s.Status = "failed"
	}

	lines := scanLines(combined)
	unchanged := 0
	var failures []storage.Failure

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := k8sActionRe.FindStringSubmatch(trimmed); m != nil {
			action := m[2]
			switch action {
			case "created", "exposed":
				s.Added++
			case "configured", "server-side-applied":
				s.Changed++
			case "deleted":
				s.Destroyed++
			case "unchanged":
				unchanged++
			}
			continue
		}
		if m := k8sErrorRe.FindStringSubmatch(trimmed); m != nil {
			if len(failures) < MaxSummaryFailures {
				msg := m[2]
				if m[1] != "" {
					msg = fmt.Sprintf("%s: %s", m[1], m[2])
				}
				failures = append(failures, storage.Failure{
					Message: msg,
					Excerpt: trimmed,
				})
			}
			continue
		}
		// Detect failing pods in `kubectl get pods` table output
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 && fields[0] != "NAME" {
			status := fields[2]
			if status == "CrashLoopBackOff" || status == "Error" || status == "ImagePullBackOff" || status == "ContainerCannotRun" {
				if len(failures) < MaxSummaryFailures {
					ready := ""
					if len(fields) > 1 {
						ready = fmt.Sprintf(" (ready: %s)", fields[1])
					}
					failures = append(failures, storage.Failure{
						Name:    fields[0],
						Message: status + ready,
						Excerpt: trimmed,
					})
				}
			}
		}
	}

	s.Failures = failures
	if len(failures) > 0 || exitCode != 0 {
		s.Status = "failed"
	}

	var parts []string
	if s.Added > 0 {
		parts = append(parts, fmt.Sprintf("%d created", s.Added))
	}
	if s.Changed > 0 {
		parts = append(parts, fmt.Sprintf("%d configured", s.Changed))
	}
	if s.Destroyed > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", s.Destroyed))
	}
	if unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", unchanged))
	}
	if len(parts) > 0 {
		s.Details = strings.Join(parts, ", ")
	} else if len(failures) > 0 {
		s.Details = failures[0].Message
	} else if exitCode == 0 {
		s.Details = "ok"
	} else {
		s.Details = "failed"
	}

	return s
}

// --- 6. terraform ---

var (
	tfPlanRe     = regexp.MustCompile(`Plan:\s+(\d+)\s+to add,\s+(\d+)\s+to change,\s+(\d+)\s+to destroy\.`)
	tfApplyRe    = regexp.MustCompile(`Apply complete!\s+Resources:\s+(\d+)\s+added,\s+(\d+)\s+changed,\s+(\d+)\s+destroyed\.`)
	tfDestroyRe  = regexp.MustCompile(`Destroy complete!\s+Resources:\s+(\d+)\s+destroyed\.`)
	tfLocRe      = regexp.MustCompile(`on\s+(\S+\s+line\s+\d+|\S+:\d+)`)
	tfErrStartRe = regexp.MustCompile(`^(?:│\s*)?Error:\s*(.+)`)
)

func formatTerraform(_ string, stdout, stderr string, exitCode int) *storage.CommandSummary {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	s := &storage.CommandSummary{
		Family: "terraform",
		Status: "ok",
	}
	if exitCode != 0 {
		s.Status = "failed"
	}

	lines := scanLines(combined)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := tfPlanRe.FindStringSubmatch(trimmed); m != nil {
			s.Added, _ = strconv.Atoi(m[1])
			s.Changed, _ = strconv.Atoi(m[2])
			s.Destroyed, _ = strconv.Atoi(m[3])
			if s.Added+s.Changed+s.Destroyed > 0 {
				s.Status = "changes_planned"
			} else {
				s.Status = "no_changes"
			}
			s.Details = fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy", s.Added, s.Changed, s.Destroyed)
			break
		}
		if m := tfApplyRe.FindStringSubmatch(trimmed); m != nil {
			s.Added, _ = strconv.Atoi(m[1])
			s.Changed, _ = strconv.Atoi(m[2])
			s.Destroyed, _ = strconv.Atoi(m[3])
			s.Status = "ok"
			s.Details = fmt.Sprintf("Apply complete: %d added, %d changed, %d destroyed", s.Added, s.Changed, s.Destroyed)
			break
		}
		if m := tfDestroyRe.FindStringSubmatch(trimmed); m != nil {
			s.Destroyed, _ = strconv.Atoi(m[1])
			s.Status = "ok"
			s.Details = fmt.Sprintf("Destroy complete: %d destroyed", s.Destroyed)
			break
		}
		if strings.Contains(trimmed, "No changes. Your infrastructure matches the configuration") ||
			strings.Contains(trimmed, "No changes. Infrastructure is up-to-date") {
			s.Status = "no_changes"
			s.Details = "No changes"
			break
		}
		if strings.Contains(trimmed, "Success! The configuration is valid") {
			s.Status = "valid"
			s.Details = "Configuration is valid"
			break
		}
	}

	// Extract error blocks
	var failures []storage.Failure
	var currentErr *storage.Failure
	var currentExcerpt []string

	finishErr := func() {
		if currentErr != nil {
			if len(currentExcerpt) > 0 {
				currentErr.Excerpt = boundExcerpt(currentExcerpt)
			}
			failures = append(failures, *currentErr)
			currentErr = nil
			currentExcerpt = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		cleanLine := strings.TrimPrefix(trimmed, "│ ")
		cleanLine = strings.TrimPrefix(cleanLine, "│")

		if m := tfErrStartRe.FindStringSubmatch(trimmed); m != nil {
			finishErr()
			if len(failures) < MaxSummaryFailures {
				currentErr = &storage.Failure{Message: m[1]}
			}
			continue
		}
		if currentErr != nil {
			if strings.HasPrefix(trimmed, "╵") {
				finishErr()
				continue
			}
			if len(currentExcerpt) < MaxFailureExcerptLine {
				currentExcerpt = append(currentExcerpt, cleanLine)
			}
			if locM := tfLocRe.FindStringSubmatch(cleanLine); locM != nil && currentErr.Location == "" {
				currentErr.Location = locM[1]
			}
		}
	}
	finishErr()

	s.Failures = failures
	if len(failures) > 0 || exitCode != 0 {
		s.Status = "failed"
		if s.Details == "" {
			if len(failures) > 0 {
				s.Details = failures[0].Message
			} else {
				s.Details = "failed"
			}
		}
	}

	return s
}

// Helper utilities
func scanLines(data string) []string {
	if data == "" {
		return nil
	}
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func boundExcerpt(lines []string) string {
	if len(lines) > MaxFailureExcerptLine {
		lines = lines[:MaxFailureExcerptLine]
	}
	return strings.Join(lines, "\n")
}
