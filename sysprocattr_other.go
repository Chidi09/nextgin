//go:build !unix && !js

package nextgin

import "syscall"

func workerSysProcAttr() *syscall.SysProcAttr { return nil }
