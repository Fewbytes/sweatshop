package viceroy

import "os"

func writeExec(path, body string) error { return os.WriteFile(path, []byte(body), 0o755) }
