//go:build linux && !js

package nextgin

import "syscall"

// Own process group so a terminal Ctrl-C reaches only Go, which then stops
// workers in order; Pdeathsig cleans up workers if Go is killed abruptly.
func workerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
