//go:build !windows

package main

import "syscall"

// detachedProcAttr 返回让子进程脱离父会话的属性（父进程退出/exec 后子进程仍存活）。
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
