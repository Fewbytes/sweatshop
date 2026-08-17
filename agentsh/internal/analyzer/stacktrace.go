package analyzer

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	pyTracebackStart = regexp.MustCompile(`^Traceback \(most recent call last\):`)
	pyFrameLine      = regexp.MustCompile(`^\s+File "([^"]+)", line (\d+), in (\S+)`)
	pyErrorLine      = regexp.MustCompile(`^([a-zA-Z0-9_.]+(?:Error|Exception|Interrupt|Exit|Warning)):\s*(.*)`)

	goPanicStart  = regexp.MustCompile(`^(?:panic:|fatal error:|goroutine \d+ \[[^\]]+\]:)`)
	goFuncLine    = regexp.MustCompile(`^([a-zA-Z0-9_./\\*-]+\.[a-zA-Z0-9_]+(?:\(.*\))?)$`)
	goFileLocLine = regexp.MustCompile(`^\s+([a-zA-Z0-9_./\\-]+\.go:\d+)(?:\s+\+0x[0-9a-fA-F]+)?`)

	nodeErrorStart = regexp.MustCompile(`^(?:[a-zA-Z0-9_.]*Error|Exception):\s*(.+)`)
	nodeFrameLine  = regexp.MustCompile(`^\s+at\s+(?:.*?\s+)?\(?([a-zA-Z0-9_./\\-]+\.[a-zA-Z0-9]+:\d+:\d+)\)?`)

	rustBacktrace = regexp.MustCompile(`^(?:stack backtrace:|thread '.*' panicked at)`)
	rustFrameLine = regexp.MustCompile(`^\s*\d+:\s*(?:0x[0-9a-fA-F]+\s+-\s+)?(.*)`)

	javaErrorStart = regexp.MustCompile(`^[a-zA-Z0-9_.]+(?:Exception|Error):\s*(.*)`)
	javaFrameLine  = regexp.MustCompile(`^\s+at\s+([a-zA-Z0-9_.$]+)\(([a-zA-Z0-9_.]+\.java:\d+)\)`)
)

// StackTraceBlock represents a detected multi-line stack trace span.
type StackTraceBlock struct {
	Language  string
	ErrorType string
	TopFrame  string
	StartLine int
	EndLine   int
	Lines     []string
}

// DetectStackTraces scans lines and extracts multi-line stack trace blocks.
func DetectStackTraces(lines []string) []StackTraceBlock {
	var blocks []StackTraceBlock
	n := len(lines)
	i := 0

	for i < n {
		trimmed := strings.TrimSpace(lines[i])

		// 1. Python traceback
		if pyTracebackStart.MatchString(trimmed) {
			start := i
			i++
			topFrame := ""
			errType := ""
			for i < n {
				tLine := strings.TrimSpace(lines[i])
				if m := pyFrameLine.FindStringSubmatch(lines[i]); m != nil {
					if topFrame == "" {
						topFrame = fmt.Sprintf("%s:%s in %s", m[1], m[2], m[3])
					}
					i++
					if i < n && strings.HasPrefix(lines[i], "    ") {
						i++ // skip source line
					}
					continue
				}
				if m := pyErrorLine.FindStringSubmatch(tLine); m != nil {
					errType = m[1]
					i++
					break
				}
				if tLine == "" || !strings.HasPrefix(lines[i], " ") {
					break
				}
				i++
			}
			blockLines := lines[start:i]
			blocks = append(blocks, StackTraceBlock{
				Language:  "python",
				ErrorType: errType,
				TopFrame:  topFrame,
				StartLine: start + 1,
				EndLine:   i,
				Lines:     blockLines,
			})
			continue
		}

		// 2. Go stack trace
		if goPanicStart.MatchString(trimmed) {
			start := i
			errType := trimmed
			topFrame := ""
			i++
			for i < n {
				tLine := strings.TrimSpace(lines[i])
				if tLine == "" {
					if i+1 < n && (strings.HasPrefix(lines[i+1], "goroutine ") || strings.HasPrefix(lines[i+1], "\t") || goFuncLine.MatchString(strings.TrimSpace(lines[i+1]))) {
						i++
						continue
					}
					break
				}
				if strings.HasPrefix(tLine, "[signal ") {
					i++
					continue
				}
				if strings.HasPrefix(tLine, "goroutine ") {
					i++
					continue
				}
				if goFuncLine.MatchString(tLine) {
					funcName := tLine
					i++
					if i < n {
						if locM := goFileLocLine.FindStringSubmatch(lines[i]); locM != nil {
							if topFrame == "" {
								topFrame = fmt.Sprintf("%s (%s)", funcName, locM[1])
							}
							i++
							continue
						}
					}
					continue
				}
				if strings.HasPrefix(tLine, "created by ") {
					i++
					continue
				}
				if !strings.HasPrefix(lines[i], "\t") && !strings.Contains(lines[i], "(") {
					break
				}
				i++
			}
			blockLines := lines[start:i]
			blocks = append(blocks, StackTraceBlock{
				Language:  "go",
				ErrorType: errType,
				TopFrame:  topFrame,
				StartLine: start + 1,
				EndLine:   i,
				Lines:     blockLines,
			})
			continue
		}

		// 3. Node / JS stack trace
		if nodeErrorStart.MatchString(trimmed) && i+1 < n && nodeFrameLine.MatchString(lines[i+1]) {
			start := i
			errType := trimmed
			topFrame := ""
			i++
			for i < n && nodeFrameLine.MatchString(lines[i]) {
				if topFrame == "" {
					if m := nodeFrameLine.FindStringSubmatch(lines[i]); m != nil {
						topFrame = m[1]
					}
				}
				i++
			}
			blockLines := lines[start:i]
			blocks = append(blocks, StackTraceBlock{
				Language:  "node",
				ErrorType: errType,
				TopFrame:  topFrame,
				StartLine: start + 1,
				EndLine:   i,
				Lines:     blockLines,
			})
			continue
		}

		// 4. Rust backtrace
		if rustBacktrace.MatchString(trimmed) {
			start := i
			errType := trimmed
			topFrame := ""
			i++
			for i < n {
				tLine := strings.TrimSpace(lines[i])
				if rustFrameLine.MatchString(lines[i]) || strings.HasPrefix(tLine, "at ") {
					if topFrame == "" && strings.HasPrefix(tLine, "at ") {
						topFrame = strings.TrimPrefix(tLine, "at ")
					}
					i++
					continue
				}
				if tLine == "" || (!strings.HasPrefix(lines[i], " ") && !strings.HasPrefix(lines[i], "\t")) {
					break
				}
				i++
			}
			blockLines := lines[start:i]
			blocks = append(blocks, StackTraceBlock{
				Language:  "rust",
				ErrorType: errType,
				TopFrame:  topFrame,
				StartLine: start + 1,
				EndLine:   i,
				Lines:     blockLines,
			})
			continue
		}

		// 5. Java stack trace
		if javaErrorStart.MatchString(trimmed) && i+1 < n && javaFrameLine.MatchString(lines[i+1]) {
			start := i
			errType := trimmed
			topFrame := ""
			i++
			for i < n && javaFrameLine.MatchString(lines[i]) {
				if topFrame == "" {
					if m := javaFrameLine.FindStringSubmatch(lines[i]); m != nil {
						topFrame = fmt.Sprintf("%s(%s)", m[1], m[2])
					}
				}
				i++
			}
			blockLines := lines[start:i]
			blocks = append(blocks, StackTraceBlock{
				Language:  "java",
				ErrorType: errType,
				TopFrame:  topFrame,
				StartLine: start + 1,
				EndLine:   i,
				Lines:     blockLines,
			})
			continue
		}

		i++
	}

	return blocks
}
