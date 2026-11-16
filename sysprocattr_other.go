//go:build !unix

package nextgin

import "syscall"

func workerSysProcAttr() *syscall.SysProcAttr { return nil }
