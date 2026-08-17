//go:build linux

package executor

func DefaultContainment() Containment {
	cgroup, err := DetectCgroup()
	if err == nil {
		return cgroup
	}
	return ProcessGroup{}
}
