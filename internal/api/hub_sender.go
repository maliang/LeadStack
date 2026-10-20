package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maliang/leadstack/internal/jobs"
	"github.com/maliang/leadstack/pkg/protocol"
)

// JobRouter 接收 agent 侧 job.* 帧，转发给任务下发器（jobs.Dispatcher 实现）。
//
// 只声明 检测 需要的四个方法，避免 api 依赖 jobs 的其余实现细节。
type JobRouter interface {
	HandleAck(serverID, runID string)
	HandleLog(serverID, runID string, seq int, stream, chunk string)
	HandleDone(serverID, runID string, exitCode int, errMsg string)
	FailServer(serverID string, cause error)
}

// jobFrameResult 描述一次 job 帧路由的结果，便于日志与测试断言。
type jobFrameResult struct {
	Handled bool
	RunID   string
}

// HubSender 把 jobs.Sender 适配到 WebSocket Hub：SendJob 直接写对应连接。
type HubSender struct {
	hub *Hub

	mu       sync.RWMutex
	observer func(serverID, runID string)
}

// SetSendJobObserver 注册「一次 job.exec 已下发」的观察者（用于发布日志旁路绑定 runID）。
func (s *HubSender) SetSendJobObserver(fn func(serverID, runID string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.observer = fn
	s.mu.Unlock()
}

// notifyObserver 通知观察者（不存在时静默跳过）。
func (s *HubSender) notifyObserver(serverID, runID string) {
	if s == nil {
		return
	}
	s.mu.RLock()
	fn := s.observer
	s.mu.RUnlock()
	if fn != nil && runID != "" {
		fn(serverID, runID)
	}
}

// 编译期确认接口实现。
var _ jobs.Sender = (*HubSender)(nil)

// NewHubSender 创建 Hub 侧的 jobs.Sender 适配器。
func NewHubSender(hub *Hub) *HubSender {
	return &HubSender{hub: hub}
}

// SendJob 把 job.exec 信封发给指定服务器；离线返回 jobs.ErrServerOffline。
func (s *HubSender) SendJob(serverID string, envelope []byte) error {
	if s == nil || s.hub == nil {
		return jobs.ErrServerOffline
	}
	conn, ok := s.hub.Get(serverID)
	if !ok {
		return fmt.Errorf("发送任务到服务器 %s: %w", serverID, jobs.ErrServerOffline)
	}
	// 先绑定 runID（若有部署日志旁路在等待），再写帧，保证不会漏掉紧随其后的 job.log。
	s.notifyObserver(serverID, extractRunIDFromEnvelope(envelope))
	if err := conn.writeRaw(envelope); err != nil {
		return fmt.Errorf("发送任务到服务器 %s: %w: %v", serverID, jobs.ErrSendFailed, err)
	}
	return nil
}

// IsOnline 返回服务器是否持有活跃的 agent 连接。
func (s *HubSender) IsOnline(serverID string) bool {
	if s == nil || s.hub == nil {
		return false
	}
	_, ok := s.hub.Get(serverID)
	return ok
}

// writeRaw 直接发送一段已序列化的 JSON 文本帧。
func (c *agentConn) writeRaw(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(agentWriteTimeout)); err != nil {
		return fmt.Errorf("设置写超时: %w", err)
	}
	if err := c.ws.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("写入 WebSocket 帧: %w", err)
	}
	return nil
}

// dispatchJobFrame 把 agent 的 job.* 帧转交给 Dispatcher（契约第 14.2 节）。
//
// 返回 handled=false 表示该类型不由本函数处理（例如未装配下发器），调用方按「忽略未知类型」处理。
func (a *API) dispatchJobFrame(serverID string, env protocol.Envelope) (jobFrameResult, error) {
	if a.jobs == nil {
		// 未装配任务下发器时保持 基础 行为：忽略但不报错、不断开连接。
		return jobFrameResult{Handled: false}, nil
	}
	if serverID == "" {
		return jobFrameResult{Handled: false}, errors.New("收到 job 帧但连接尚未绑定服务器")
	}

	switch env.Type {
	case protocol.TypeJobAck:
		var ack protocol.JobAck
		if len(env.Payload) > 0 {
			if err := json.Unmarshal(env.Payload, &ack); err != nil {
				return jobFrameResult{}, fmt.Errorf("解析 job.ack 载荷: %w", err)
			}
		}
		if ack.RunID == "" {
			return jobFrameResult{}, errors.New("job.ack 缺少 run_id")
		}
		a.jobs.HandleAck(serverID, ack.RunID)
		return jobFrameResult{Handled: true, RunID: ack.RunID}, nil

	case protocol.TypeJobLog:
		var jobLog protocol.JobLog
		if len(env.Payload) > 0 {
			if err := json.Unmarshal(env.Payload, &jobLog); err != nil {
				return jobFrameResult{}, fmt.Errorf("解析 job.log 载荷: %w", err)
			}
		}
		if jobLog.RunID == "" {
			return jobFrameResult{}, errors.New("job.log 缺少 run_id")
		}
		stream := jobLog.Stream
		if stream != "stdout" && stream != "stderr" {
			stream = "stdout" // 未知流按 stdout 归档，避免丢日志
		}
		a.jobs.HandleLog(serverID, jobLog.RunID, jobLog.Seq, stream, jobLog.Chunk)
		return jobFrameResult{Handled: true, RunID: jobLog.RunID}, nil

	case protocol.TypeJobDone:
		var done protocol.JobDone
		if len(env.Payload) > 0 {
			if err := json.Unmarshal(env.Payload, &done); err != nil {
				return jobFrameResult{}, fmt.Errorf("解析 job.done 载荷: %w", err)
			}
		}
		if done.RunID == "" {
			return jobFrameResult{}, errors.New("job.done 缺少 run_id")
		}
		a.jobs.HandleDone(serverID, done.RunID, done.ExitCode, done.Error)
		return jobFrameResult{Handled: true, RunID: done.RunID}, nil

	default:
		return jobFrameResult{Handled: false}, nil
	}
}

// failServerJobs 在 agent 连接断开时唤醒所有等待者。
func (a *API) failServerJobs(serverID string, cause error) {
	if a.jobs == nil || serverID == "" {
		return
	}
	if cause == nil {
		cause = errors.New("agent 连接已断开")
	}
	a.jobs.FailServer(serverID, cause)
}
