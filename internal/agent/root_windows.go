//go:build windows

package agent

import "os"

// rootPath 返回 Windows 系统盘路径（disk.Usage 需要盘符）。
func rootPath() string {
	if drive := os.Getenv("SystemDrive"); drive != "" {
		return drive + `\`
	}
	return `C:\`
}
