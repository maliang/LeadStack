package agent

import (
	"sync"
	"time"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	// 环形缓冲默认容量（按 15s 间隔、5 个指标、保留 15 分钟估算约 300 条，
	// 这里留足余量以覆盖更短的上报间隔）。
	defaultBufferCapacity = 4096
	// 断线期间指标的最长保留时间。
	defaultBufferRetention = 15 * time.Minute
)

// sampleBuffer 是保存待发送指标样本的环形缓冲。
//
// 断线期间样本在此累积，重连后按原始时间戳补传；超过保留窗口（默认 15 分钟）
// 或超出容量的旧样本会被丢弃（容量满时覆盖最旧的一条）。
type sampleBuffer struct {
	mu        sync.Mutex
	buf       []protocol.Sample
	head      int // 最旧元素下标
	size      int
	retention time.Duration
	now       func() time.Time // 便于测试注入时钟
}

// newSampleBuffer 创建指定容量与保留窗口的环形缓冲。
func newSampleBuffer(capacity int, retention time.Duration) *sampleBuffer {
	if capacity <= 0 {
		capacity = defaultBufferCapacity
	}
	if retention <= 0 {
		retention = defaultBufferRetention
	}
	return &sampleBuffer{
		buf:       make([]protocol.Sample, capacity),
		retention: retention,
		now:       time.Now,
	}
}

// Add 追加上报样本，并丢弃过期数据。
func (b *sampleBuffer) Add(samples []protocol.Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range samples {
		b.pushLocked(s)
	}
	b.dropExpiredLocked()
}

// pushLocked 写入一条样本；缓冲满时覆盖最旧的一条。
func (b *sampleBuffer) pushLocked(s protocol.Sample) {
	if b.size == len(b.buf) {
		b.buf[b.head] = s
		b.head = (b.head + 1) % len(b.buf)
		return
	}
	idx := (b.head + b.size) % len(b.buf)
	b.buf[idx] = s
	b.size++
}

// dropExpiredLocked 从最旧一端丢弃超过保留窗口的样本。
func (b *sampleBuffer) dropExpiredLocked() {
	cutoff := b.now().UnixMilli() - b.retention.Milliseconds()
	for b.size > 0 {
		if b.buf[b.head].TS >= cutoff {
			return
		}
		b.head = (b.head + 1) % len(b.buf)
		b.size--
	}
}

// Drain 取出全部样本并清空缓冲，保持时间顺序。
func (b *sampleBuffer) Drain() []protocol.Sample {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropExpiredLocked()
	if b.size == 0 {
		return nil
	}
	out := make([]protocol.Sample, 0, b.size)
	for i := 0; i < b.size; i++ {
		out = append(out, b.buf[(b.head+i)%len(b.buf)])
	}
	b.head = 0
	b.size = 0
	return out
}

// Len 返回当前缓冲中的样本数（已排除过期数据）。
func (b *sampleBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropExpiredLocked()
	return b.size
}
