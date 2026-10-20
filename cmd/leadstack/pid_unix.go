//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// processAlive 判断进程是否仍存活（unix：signal 0 探测，EPERM 说明进程存在但无权限）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
