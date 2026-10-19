package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/maliang/leadstack/internal/hostinfo"
	"github.com/maliang/leadstack/internal/store"
	"github.com/maliang/leadstack/pkg/protocol"
)

// TestResult 是 POST /api/v1/servers/{id}/test-connection 的业务载荷。
//
// 注意：SSH 层失败属于「测试结果」，不是 HTTP 错误——接口仍返回 200，由 ok=false 表达。
type TestResult struct {
	OK                 bool   `json:"ok"`
	LatencyMS          int64  `json:"latency_ms"`
	SSHBanner          string `json:"ssh_banner"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	Error              string `json:"error,omitempty"`
}

// TestConnection 对指定服务器做一次真实 SSH 握手，返回延迟、banner 与主机密钥指纹。
func (c *Collector) TestConnection(ctx context.Context, serverID string) (TestResult, error) {
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return TestResult{}, err // ErrNotFound 由 API 层映射为 404
	}

	secret, err := c.secretFor(ctx, *srv)
	if err != nil {
		return TestResult{OK: false, Error: err.Error()}, nil
	}

	start := time.Now()
	client, err := c.dial(ctx, *srv, secret)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		c.logger.Info("连通性测试失败", "server_id", serverID, "latency_ms", latency, "err", err)
		return TestResult{OK: false, LatencyMS: latency, Error: err.Error()}, nil
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			c.logger.Debug("关闭 SSH 连接失败", "server_id", serverID, "err", cerr)
		}
	}()

	result := TestResult{
		OK:                 true,
		LatencyMS:          latency,
		SSHBanner:          client.ServerVersion(),
		HostKeyFingerprint: client.HostKeyFingerprint(),
	}
	c.logger.Info("连通性测试成功",
		"server_id", serverID, "latency_ms", latency,
		"banner", result.SSHBanner, "fingerprint", result.HostKeyFingerprint)
	return result, nil
}

// RefreshHostInfo 通过 SSH 采集主机信息并落库，返回采集结果。
func (c *Collector) RefreshHostInfo(ctx context.Context, serverID string) (protocol.HostInfo, error) {
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return protocol.HostInfo{}, err
	}

	secret, err := c.secretFor(ctx, *srv)
	if err != nil {
		return protocol.HostInfo{}, err
	}

	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	client, err := c.dial(opCtx, *srv, secret)
	if err != nil {
		return protocol.HostInfo{}, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			c.logger.Debug("关闭 SSH 连接失败", "server_id", serverID, "err", cerr)
		}
	}()

	info, err := hostinfo.Collect(opCtx, client)
	if err != nil {
		return protocol.HostInfo{}, fmt.Errorf("采集主机信息失败: %w", err)
	}
	if info.CollectedAt == 0 {
		info.CollectedAt = store.NowMS()
	}
	c.saveHostInfo(opCtx, serverID, info)

	c.logger.Info("主机信息已刷新",
		"server_id", serverID, "hostname", info.Hostname, "os", info.OS, "arch", info.Arch)
	return toProtocolHostInfo(info), nil
}

// toProtocolHostInfo 把 hostinfo.Info 转成协议结构（字段一一对应，json tag 相同）。
func toProtocolHostInfo(info hostinfo.Info) protocol.HostInfo {
	return protocol.HostInfo{
		Hostname:       info.Hostname,
		OS:             info.OS,
		OSVersion:      info.OSVersion,
		Kernel:         info.Kernel,
		Arch:           info.Arch,
		CPUModel:       info.CPUModel,
		CPUCores:       info.CPUCores,
		MemTotalBytes:  info.MemTotalBytes,
		DiskTotalBytes: info.DiskTotalBytes,
		DiskUsedBytes:  info.DiskUsedBytes,
		UptimeS:        info.UptimeS,
		Timezone:       info.Timezone,
		Docker:         info.Docker,
		DockerVersion:  info.DockerVersion,
		IPAddrs:        info.IPAddrs,
		CollectedAt:    info.CollectedAt,
	}
}

// jsonMarshal 是 json.Marshal 的窄封装，便于集中替换实现。
func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("序列化 JSON: %w", err)
	}
	return b, nil
}
