//go:build !linux

package executor

func DefaultContainment() Containment { return ProcessGroup{} }
