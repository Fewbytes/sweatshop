//go:build !unix

package main

import "syscall"

func detachedProcessAttributes() *syscall.SysProcAttr { return nil }
