//go:build windows

package main

import "syscall"

// detachedProcAttr 在 Windows 上不设置额外属性（Windows 不支持自升级，此路径仅用于普通重启）。
func detachedProcAttr() *syscall.SysProcAttr {
	return nil
}
