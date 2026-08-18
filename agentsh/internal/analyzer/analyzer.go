package analyzer

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
)

// DefaultMaxAnalysisBytes bounds how much of a stream AnalyzeReader will scan.
// It keeps peak memory for template analysis independent of blob size: a
// multi-GB log is capped rather than read in full.
const DefaultMaxAnalysisBytes = 64 * 1024 * 1024 // 64MB

// maxLineBytes bounds a single scanned line. Without a cap, bufio.Scanner's
// default 64KB token limit makes it silently stop scanning (ErrTooLong) on a
// single huge line; this cap is generous but still finite.
const maxLineBytes = 1 << 20 // 1MB

// AnalyzeStream processes the raw text of a log stream (stdout or stderr) and returns
// a complete structured LogAnalysis containing Drain-clustered templates, collapsed
// stack traces, and a level histogram. It reads content already resident in memory;
// for reading a stream from disk without buffering it all upfront, use AnalyzeReader.
func AnalyzeStream(invocationID, stream, content string) *LogAnalysis {
	if content == "" {
		return emptyAnalysis(invocationID, stream)
	}
	lines, offsets := scanLinesWithOffsets(strings.NewReader(content))
	return analyzeLines(invocationID, stream, lines, offsets)
}

// AnalyzeReader scans a log stream directly from r, avoiding a full-content
// read+copy of the underlying blob. Scanning stops after maxBytes (or
// DefaultMaxAnalysisBytes if maxBytes <= 0); Truncated on the result reports
// whether trailing content was left unscanned.
func AnalyzeReader(invocationID, stream string, r io.Reader, maxBytes int64) *LogAnalysis {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxAnalysisBytes
	}
	lines, offsets := scanLinesWithOffsets(io.LimitReader(r, maxBytes))
	if len(lines) == 0 {
		return emptyAnalysis(invocationID, stream)
	}

	var probe [1]byte
	n, _ := r.Read(probe[:])
	truncated := n > 0

	analysis := analyzeLines(invocationID, stream, lines, offsets)
	analysis.Truncated = truncated
	return analysis
}

func emptyAnalysis(invocationID, stream string) *LogAnalysis {
	return &LogAnalysis{
		InvocationID: invocationID,
		Stream:       stream,
		TotalLines:   0,
		Templates:    nil,
		Levels:       make(map[string]int),
	}
}

func analyzeLines(invocationID, stream string, lines []string, offsets []int64) *LogAnalysis {
	totalLines := len(lines)
	levels := ComputeLevelHistogram(lines)

	// 1. Detect multi-line stack traces
	stBlocks := DetectStackTraces(lines)
	inStackTrace := make(map[int]bool)
	var stTemplates []LogTemplate

	for _, block := range stBlocks {
		for l := block.StartLine; l <= block.EndLine; l++ {
			inStackTrace[l] = true
		}

		top := block.TopFrame
		if top == "" {
			top = block.ErrorType
		}
		tmplStr := fmt.Sprintf("[stack trace: %s at %s]", block.Language, top)
		tmplID := GenerateTemplateID(tmplStr)

		var offset int64
		if block.StartLine-1 < len(offsets) {
			offset = offsets[block.StartLine-1]
		}

		exemplar := strings.Join(block.Lines, "\n")
		// Bound very large exemplar stack traces to max 20 lines
		if len(block.Lines) > 20 {
			exemplar = strings.Join(block.Lines[:20], "\n") + "\n..."
		}

		stTemplates = append(stTemplates, LogTemplate{
			ID:             tmplID,
			Template:       tmplStr,
			Count:          1,
			FirstLine:      block.StartLine,
			LastLine:       block.EndLine,
			ExemplarOffset: offset,
			Exemplar:       exemplar,
			Level:          "ERROR",
			IsStackTrace:   true,
		})
	}

	// 2. Filter lines that were not absorbed by stack traces
	var nonSTLines []string
	var nonSTOffsets []int64
	var nonSTLineNums []int

	for idx, line := range lines {
		lineNum := idx + 1
		if inStackTrace[lineNum] {
			continue
		}
		nonSTLines = append(nonSTLines, line)
		if idx < len(offsets) {
			nonSTOffsets = append(nonSTOffsets, offsets[idx])
		}
		nonSTLineNums = append(nonSTLineNums, lineNum)
	}

	// 3. Cluster remaining lines into templates
	clusters := ClusterLines(nonSTLines, nonSTOffsets)

	// Map line numbers from cluster indices
	templatesMap := make(map[string]*LogTemplate)
	var order []string

	for _, c := range clusters {
		t := &LogTemplate{
			ID:             c.ID,
			Template:       c.Template,
			Count:          c.Count,
			FirstLine:      c.FirstLine,
			LastLine:       c.LastLine,
			ExemplarOffset: c.ExemplarOffset,
			Exemplar:       c.Exemplar,
			Level:          c.Level,
			IsStackTrace:   false,
		}
		templatesMap[c.ID] = t
		order = append(order, c.ID)
	}

	// Merge stack trace templates (aggregating duplicates if identical stack trace repeated)
	for _, st := range stTemplates {
		if existing, exists := templatesMap[st.ID]; exists {
			existing.Count++
			existing.LastLine = st.LastLine
		} else {
			templatesMap[st.ID] = &st
			order = append(order, st.ID)
		}
	}

	// Build sorted templates list (stacktraces / errors first, then highest frequency)
	templates := make([]LogTemplate, 0, len(order))
	for _, id := range order {
		templates = append(templates, *templatesMap[id])
	}

	sort.SliceStable(templates, func(i, j int) bool {
		ti, tj := templates[i], templates[j]
		priI := templatePriority(ti)
		priJ := templatePriority(tj)
		if priI != priJ {
			return priI > priJ
		}
		return ti.Count > tj.Count
	})

	return &LogAnalysis{
		InvocationID: invocationID,
		Stream:       stream,
		TotalLines:   totalLines,
		Templates:    templates,
		Levels:       levels,
	}
}

func scanLinesWithOffsets(r io.Reader) ([]string, []int64) {
	var lines []string
	var offsets []int64

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	var currentOffset int64
	for scanner.Scan() {
		offsets = append(offsets, currentOffset)
		text := scanner.Text()
		lines = append(lines, text)
		currentOffset += int64(len(text) + 1) // +1 for '\n'
	}
	return lines, offsets
}
