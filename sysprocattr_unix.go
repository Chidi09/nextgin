//go:build unix && !linux && !js

package nextgin

import "syscall"

func workerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
