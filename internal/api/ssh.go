package api

import (
	"context"

	"github.com/maliang/leadstack/internal/collector"
	"github.com/maliang/leadstack/pkg/protocol"
)

// SSHService 是 API 层对 SSH 能力的依赖。
//
// 由 internal/collector 的采集器实现（cmd/leadstack 负责注入），
// 这样接口层不直接依赖 sshx，测试里可以用替身。
type SSHService interface {
	// TestConnection 对指定服务器做一次真实 SSH 握手。
	TestConnection(ctx context.Context, serverID string) (collector.TestResult, error)
	// RefreshHostInfo 通过 SSH 采集主机信息并落库，返回采集结果。
	RefreshHostInfo(ctx context.Context, serverID string) (protocol.HostInfo, error)
}
