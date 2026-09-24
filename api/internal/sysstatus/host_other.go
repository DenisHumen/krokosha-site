//go:build !linux

package sysstatus

import "runtime"

// readHost has nothing to read where there is no /proc: the service runs on Linux, this build
// exists so that the project compiles and its tests run on a developer's machine.
func readHost(string) Host {
	return Host{CPUs: runtime.NumCPU()}
}

// readCPUTimes has no counters to read here.
func readCPUTimes() (busy, total uint64, ok bool) { return 0, 0, false }
