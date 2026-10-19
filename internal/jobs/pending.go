package jobs

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// 输出流编号。
const (
	streamStdout = 0
	streamStderr = 1
)

// logEntry 是一条 job.log 分块。
type logEntry struct {
	stream int
	chunk  string
}

// outcome 是交给 Exec 的最终结果。
type outcome struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

// pending 表示一个正在等待 job.done 的任务。
//
// 日志分块先按 seq 存在 entries 里（乱序由最终排序解决，重复 seq 只保留第一份），
// 每个 stream 最多保留 maxOutput 字节，防止恶意/失控输出打爆内存。
type pending struct {
	serverID  string
	runID     string
	maxOutput int
	done      chan outcome

	mu         sync.Mutex
	entries    map[int]logEntry
	bytes      [2]int
	truncated  [2]bool
	finished   bool
	duplicates int
	acked      bool
}

// newPending 创建等待者；done 带 1 个缓冲，保证交付方永不阻塞。
func newPending(serverID, runID string, maxOutput int) *pending {
	return &pending{
		serverID:  serverID,
		runID:     runID,
		maxOutput: maxOutput,
		done:      make(chan outcome, 1),
		entries:   make(map[int]logEntry),
	}
}

// addLog 记录一条日志分块。
// 重复 seq 只保留第一份；未知 stream 忽略；超出体积上限的部分丢弃并置截断标记。
func (p *pending) addLog(seq int, stream, chunk string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || chunk == "" {
		return
	}
	idx := streamIndex(stream)
	if idx < 0 {
		return
	}
	if _, dup := p.entries[seq]; dup {
		p.duplicates++
		return
	}
	remaining := p.maxOutput - p.bytes[idx]
	if remaining <= 0 {
		p.truncated[idx] = true
		return
	}
	if len(chunk) > remaining {
		chunk = cutRune(chunk, remaining)
		p.truncated[idx] = true
	}
	p.entries[seq] = logEntry{stream: idx, chunk: chunk}
	p.bytes[idx] += len(chunk)
}

// markAck 记录 agent 已接手任务。
func (p *pending) markAck() {
	p.mu.Lock()
	p.acked = true
	p.mu.Unlock()
}

// buildOutcome 组装结果并标记结束。
// 返回 false 表示该任务已结束（重复 job.done / FailServer 与 done 竞争）。
func (p *pending) buildOutcome(exitCode int, errMsg string) (outcome, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return outcome{}, false
	}
	p.finished = true
	o := outcome{
		stdout:   p.assembleLocked(streamStdout),
		stderr:   p.assembleLocked(streamStderr),
		exitCode: exitCode,
	}
	if errMsg != "" {
		o.err = errors.New(errMsg)
	}
	return o, true
}

// send 把结果投递给 Exec；缓冲为 1，重复投递直接丢弃。
func (p *pending) send(o outcome) {
	select {
	case p.done <- o:
	default:
	}
}

// assembleLocked 按 seq 升序拼接某个 stream 的内容，并在发生截断时追加标记。
// 调用方必须持有 p.mu。
func (p *pending) assembleLocked(idx int) string {
	seqs := make([]int, 0, len(p.entries))
	for seq, entry := range p.entries {
		if entry.stream == idx {
			seqs = append(seqs, seq)
		}
	}
	sort.Ints(seqs)

	var b strings.Builder
	for _, seq := range seqs {
		b.WriteString(p.entries[seq].chunk)
	}
	out := b.String()
	if p.truncated[idx] && !strings.HasSuffix(out, truncatedMarker) {
		out += truncatedMarker
	}
	return out
}

// streamIndex 把 stream 名映射为输出流编号；未知 stream 返回 -1（忽略）。
func streamIndex(stream string) int {
	switch stream {
	case "stdout":
		return streamStdout
	case "stderr":
		return streamStderr
	default:
		return -1
	}
}

// cutRune 把字符串截断到至多 n 字节，且不产生非法 UTF-8（不切断多字节字符）。
func cutRune(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
