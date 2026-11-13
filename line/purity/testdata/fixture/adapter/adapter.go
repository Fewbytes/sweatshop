// Package adapter is the effectful edge of the fixture.
package adapter

import (
	"math/rand"
	"net/http"
	"os"
	"time"
)

var hits int

func Now() time.Time { return time.Now() }

func ReadConfig(path string) ([]byte, error) { return os.ReadFile(path) }

func Roll() int { return rand.Intn(6) }

func Fetch(url string) error {
	_, err := http.Get(url)
	return err
}

func HomeDir() string { return os.Getenv("HOME") }

func Hit() { hits++ }

func Peek() int { return hits }

func Fill(dst []int) { dst[0] = 1 }

// Format only transforms its argument, so it is deterministic although the
// package is an adapter.
func Format(name string) string { return "<" + name + ">" }

// Elapsed takes its clock as an argument, the injection seam.
func Elapsed(start time.Time, now func() time.Time) time.Duration { return now().Sub(start) }

// IsMissing only inspects its argument.
func IsMissing(err error) bool { return os.IsNotExist(err) }

type Ticker struct{ last time.Time }

func (t *Ticker) Elapsed() time.Duration { return t.tick().Sub(t.last) }

func (t *Ticker) tick() time.Time { return time.Now() }
