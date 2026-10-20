package api

import (
	"context"
	"errors"
	"io"
	"strconv"

	"github.com/maliang/leadstack/internal/sshx"
)

// RemoteExecutor 是「在目标机上执行一条命令」的端口（与 remote.Runner 同形）。
//
// 由 cmd 注入：ssh 模式与 agent 模式由同一个 RoutingExecutor 负责路由。
type RemoteExecutor interface {
	Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error)
}

// SFTPFactory 打开某台服务器的 SFTP 会话（ssh 模式的文件通道）。
type SFTPFactory interface {
	OpenSFTP(ctx context.Context, serverID string) (SFTPClient, error)
}

// SFTPClient 是文件接口需要的最小 SFTP 能力（sshx.SFTPSession 实现）。
type SFTPClient interface {
	List(ctx context.Context, p string) ([]sshx.FileInfo, error)
	Stat(ctx context.Context, p string) (*sshx.FileInfo, error)
	Open(ctx context.Context, p string) (io.ReadCloser, error)
	Write(ctx context.Context, p string, r io.Reader, size int64) (int64, error)
	Remove(ctx context.Context, p string) error
	Close() error
}

// errNoRemoteExecutor 表示远程执行通道未装配。
var errNoRemoteExecutor = errors.New("远程执行通道未启用")

// strconvAtoi 是 strconv.Atoi 的窄封装（终端参数用）。
func strconvAtoi(s string) (int, error) { return strconv.Atoi(s) }
