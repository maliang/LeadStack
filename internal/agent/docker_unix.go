//go:build !windows

package agent

import "os"

// dockerSocketPath 是类 Unix 系统上 Docker 守护进程的默认 socket 路径。
const dockerSocketPath = "/var/run/docker.sock"

// dockerSocketExists 判断 docker socket 是否存在（存在通常说明本机运行着 Docker）。
func dockerSocketExists() bool {
	_, err := os.Stat(dockerSocketPath)
	return err == nil
}
