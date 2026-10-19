package linestore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLogsFilterBySourceAndSinceOldestFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	all := []LogLine{
		{At: t0, Source: "serve", Line: "a"},
		{At: t0.Add(time.Second), Source: "maker-1", Line: "b"},
		{At: t0.Add(2 * time.Second), Source: "serve", Line: "c"},
	}
	for _, l := range all {
		if err := s.RecordLog(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		f    LogFilter
		want []LogLine
	}{
		{"no filter", LogFilter{}, all},
		{"source", LogFilter{Source: "serve"}, []LogLine{all[0], all[2]}},
		{"since is inclusive", LogFilter{Since: t0.Add(time.Second)}, all[1:]},
		{"no match", LogFilter{Source: "zzz"}, []LogLine{}},
	}
	for _, tt := range tests {
		got, err := s.Logs(ctx, tt.f)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Logs = %+v, %v; want %+v", tt.name, got, err, tt.want)
		}
	}
}

func TestRecordLogCutsLongLinesAndKeepsOnlyTheNewestStored(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if err := s.RecordLog(ctx, LogLine{At: at, Source: "serve", Line: strings.Repeat("x", MaxLogLineBytes+500)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Logs(ctx, LogFilter{})
	if len(got) != 1 || len(got[0].Line) > MaxLogLineBytes+len("…[cut]") || !strings.HasSuffix(got[0].Line, "…[cut]") {
		t.Fatalf("long line stored as %d bytes: %q", len(got[0].Line), got[0].Line[len(got[0].Line)-10:])
	}
	for i := 0; i < MaxStoredLogs; i++ {
		if err := s.RecordLog(ctx, LogLine{At: at, Source: "serve", Line: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM component_log`).Scan(&count); err != nil || count != MaxStoredLogs {
		t.Fatalf("stored %d lines, %v; want %d", count, err, MaxStoredLogs)
	}
	if got, _ := s.Logs(ctx, LogFilter{Source: "serve"}); len(got) != MaxLogs || got[0].Line != "n" {
		t.Fatalf("the cut line outlived the bound or Logs exceeded MaxLogs: %d lines", len(got))
	}
}
