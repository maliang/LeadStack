//go:build windows

package agent

import "os"

// dockerSocketPath 是 Windows 上 Docker 引擎的默认命名管道路径。
const dockerSocketPath = `\\.\pipe\docker_engine`

// dockerSocketExists 判断 docker 命名管道是否存在（存在通常说明本机运行着 Docker）。
func dockerSocketExists() bool {
	_, err := os.Stat(dockerSocketPath)
	return err == nil
}
