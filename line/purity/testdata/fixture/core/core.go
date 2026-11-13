// Package core is business logic that should not know about effects.
package core

import "example.com/fixture/adapter"

func Add(a, b int) int { return a + b }

func Total(xs []int) int {
	sum := 0
	for _, x := range xs {
		sum = Add(sum, x)
	}
	return sum
}

func Stamp() string { return adapter.Now().String() }

func Describe() string { return "at " + Stamp() }
