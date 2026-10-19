package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/Fewbytes/sweatshop/line/linelog"
	"github.com/Fewbytes/sweatshop/line/station"
)

const (
	// logPostBuffer bounds the lines waiting to go to the station API; a
	// component never blocks on logging, so when the station is slow the
	// newest lines are dropped.
	logPostBuffer  = 256
	logPostTimeout = 10 * time.Second
)

// componentLogger returns a logger that writes to stderr and also posts each
// line, redacted, to the station API through c, so the viceroy can read it
// with `line logs`. The posting is best effort: a failure is reported on
// stderr only, because logging it would post it again.
func componentLogger(c station.Client, prefix string) *log.Logger {
	lines := make(chan string, logPostBuffer)
	go func() {
		for first := range lines {
			batch := []string{first}
		fill:
			for len(batch) < 100 {
				select {
				case l := <-lines:
					batch = append(batch, l)
				default:
					break fill
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), logPostTimeout)
			if err := c.PostLogs(ctx, batch); err != nil {
				fmt.Fprintf(os.Stderr, "sweatshop: post %d log lines to the station API: %v\n", len(batch), err)
			}
			cancel()
		}
	}()
	record := func(entry string) {
		select {
		case lines <- entry:
		default:
		}
	}
	return log.New(linelog.Tee{Out: os.Stderr, Record: record, Secrets: []string{c.Token}}, prefix, log.LstdFlags)
}

// lineLogs implements `line logs`.
func lineLogs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line logs", flag.ContinueOnError)
	since := fs.String("since", "", "only lines from the last D (36h, 7d); default all stored lines")
	stationName := fs.String("station", "", "only lines of this station (serve is the station API)")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	lines, err := c.Logs(context.Background(), *since, *stationName)
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Fprintf(stdout, "%s %s %s\n", l.At.Format(time.RFC3339), l.Source, l.Line)
	}
	return nil
}
