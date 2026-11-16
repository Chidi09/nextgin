//go:build unix && !linux

package nextgin

import "syscall"

func workerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
