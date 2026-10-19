package hostinfo

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/pkg/protocol"
)

// metricUptimeS 不在 pkg/protocol 的指标常量中，但契约要求 Agentless 返回它。
const metricUptimeS = "uptime_s"

// cpuSnapshotMaxGap 是 CPU 计数快照的最大有效期：超过该间隔说明中间漏掉了
// 多个采集窗口，沿用旧基线会把长期平均当成瞬时占用率，因此重新建立基线并跳过本次。
const cpuSnapshotMaxGap = 5 * time.Minute

// 以下命令都在远端独立执行：任意一条失败只影响对应指标。
const (
	// Linux 读 /proc/stat 的累计计数（user nice system idle iowait irq softirq）；
	// macOS 没有 /proc，退化为 ps 的进程 CPU 之和并按核数归一化。
	cpuCommand = `if [ -r /proc/stat ]; then awk '/^cpu /{printf "linux %s %s %s %s %s %s %s", $2,$3,$4,$5,$6,$7,$8; exit}' /proc/stat; else printf "ps %s %s" "$(ps -A -o %cpu= 2>/dev/null | awk '{s+=$1} END{printf "%.2f", s}')" "$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null)"; fi`

	// 输出 "total_bytes used_bytes"。
	memCommand = `if [ -r /proc/meminfo ]; then awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^MemFree:/{f=$2} /^Buffers:/{b=$2} /^Cached:/{c=$2} END{if (a=="") a=f+b+c; printf "%d %d", t*1024, (t-a)*1024}' /proc/meminfo; else total=$(sysctl -n hw.memsize 2>/dev/null); psize=$(sysctl -n hw.pagesize 2>/dev/null); [ -z "$psize" ] && psize=4096; vm_stat 2>/dev/null | awk -v total="$total" -v psize="$psize" '/Pages active/ {gsub(/\./,"",$3); active=$3} /Pages wired down/ {gsub(/\./,"",$4); wired=$4} /Pages occupied by compressor/ {gsub(/\./,"",$5); comp=$5} END {used=(active+wired+comp)*psize; printf "%d %d", total, used}'; fi`

	// 输出 1 分钟负载。
	loadCommand = `if [ -r /proc/loadavg ]; then awk '{printf "%s", $1}' /proc/loadavg; else sysctl -n vm.loadavg 2>/dev/null | awk '{printf "%s", $2}'; fi`

	// 输出根分区使用率百分比。
	diskCommand = `df -kP / 2>/dev/null | awk 'NR==2 && $2>0 {printf "%.2f", ($3/$2)*100}'`

	// 输出运行秒数。
	uptimeCommand = `if [ -r /proc/uptime ]; then awk '{printf "%d", $1}' /proc/uptime; else boot=$(sysctl -n kern.boottime 2>/dev/null | awk -F'[=,]' '{gsub(/ /,"",$2); print $2}'); now=$(date +%s 2>/dev/null); if [ -n "$boot" ] && [ -n "$now" ] && [ "$now" -ge "$boot" ] 2>/dev/null; then printf "%d" $((now-boot)); fi; fi`
)

// Sampler 持有每台服务器的上一份 CPU 计数，用于算区间占用率（无状态则首帧 CPU 为空）。
type Sampler struct {
	mu   sync.Mutex
	last map[string]cpuSnapshot
}

type cpuSnapshot struct {
	total uint64
	idle  uint64
	at    time.Time
}

// NewSampler 构造 Sampler。
func NewSampler() *Sampler {
	return &Sampler{last: make(map[string]cpuSnapshot)}
}

// Collect 返回 cpu.pct / mem.used_pct / mem.used_bytes / load.1 / disk.used.pct / uptime_s。
// 单项失败跳过该指标而不是整体报错；serverID 用于隔离各服务器的上一次采样。
func (s *Sampler) Collect(ctx context.Context, serverID string, r sshx.Runner) ([]protocol.Sample, error) {
	if s == nil {
		return nil, errors.New("hostinfo: Sampler 不能为 nil")
	}
	if r == nil {
		return nil, errors.New("hostinfo: runner 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.last == nil {
		s.mu.Lock()
		s.last = make(map[string]cpuSnapshot)
		s.mu.Unlock()
	}

	now := time.Now()
	ts := now.UnixMilli()
	samples := make([]protocol.Sample, 0, 6)
	var firstErr error

	run := func(cmd string) (string, bool) {
		out, _, _, err := r.Run(ctx, cmd)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return "", false
		}
		return strings.TrimSpace(out), true
	}
	add := func(metric string, value float64) {
		samples = append(samples, protocol.Sample{Metric: metric, Value: value, TS: ts})
	}

	// CPU：Linux 需要上一帧计数算区间占用率；首帧/跨窗口无样本时跳过。
	if out, ok := run(cpuCommand); ok {
		if pct, ok := s.cpuPercent(serverID, out, now); ok {
			add(protocol.MetricCPUPct, pct)
		}
	}

	// 内存。
	if out, ok := run(memCommand); ok {
		if used, total, ok := parseMemUsage(out); ok && total > 0 {
			if used > total {
				used = total
			}
			add(protocol.MetricMemUsedB, float64(used))
			add(protocol.MetricMemUsedPct, float64(used)/float64(total)*100)
		}
	}

	// 负载。
	if out, ok := run(loadCommand); ok {
		if fields := strings.Fields(out); len(fields) > 0 {
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil && v >= 0 {
				add(protocol.MetricLoad1, v)
			}
		}
	}

	// 根分区使用率。
	if out, ok := run(diskCommand); ok {
		if v, err := strconv.ParseFloat(out, 64); err == nil && v >= 0 {
			add(protocol.MetricDiskUsedPt, v)
		}
	}

	// 运行时长。
	if out, ok := run(uptimeCommand); ok {
		if v, err := strconv.ParseInt(out, 10, 64); err == nil && v >= 0 {
			add(metricUptimeS, float64(v))
		}
	}

	if len(samples) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("hostinfo: 所有指标采集均失败")
	}
	return samples, nil
}

// cpuPercent 根据命令输出计算区间 CPU 占用率。
//   - Linux（"linux ..."）：用 /proc/stat 累计计数差分；首帧、计数器回退、
//     跨采集窗口无样本（间隔超过 cpuSnapshotMaxGap）或 deltaTotal 为 0 时返回 false。
//   - macOS（"ps sum ncpu"）：ps 的进程 %CPU 之和按核数归一化，单帧即可得值。
func (s *Sampler) cpuPercent(serverID, out string, now time.Time) (float64, bool) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return 0, false
	}

	switch fields[0] {
	case "linux":
		if len(fields) < 8 {
			return 0, false
		}
		vals := make([]uint64, 7)
		for i := range vals {
			v, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return 0, false
			}
			vals[i] = v
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		idle := vals[3] + vals[4] // idle + iowait

		s.mu.Lock()
		prev, ok := s.last[serverID]
		s.last[serverID] = cpuSnapshot{total: total, idle: idle, at: now}
		s.mu.Unlock()

		// 首帧没有上一份计数；或上一次采样已跨过多个窗口，基线不可用。
		if !ok || now.Sub(prev.at) > cpuSnapshotMaxGap {
			return 0, false
		}
		// 计数器回退（机器重启、热插拔或溢出）时重新建立基线。
		if total <= prev.total || idle < prev.idle {
			return 0, false
		}
		deltaTotal := total - prev.total
		deltaIdle := idle - prev.idle
		if deltaTotal == 0 || deltaIdle > deltaTotal {
			return 0, false
		}
		pct := float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
		return clampPercent(pct), true

	case "ps":
		sum, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || sum < 0 {
			return 0, false
		}
		ncpu := 1
		if len(fields) >= 3 {
			if n, err := strconv.Atoi(fields[2]); err == nil && n > 0 {
				ncpu = n
			}
		}
		return clampPercent(sum / float64(ncpu)), true

	default:
		return 0, false
	}
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// parseMemUsage 解析 "total_bytes used_bytes"。
func parseMemUsage(out string) (used, total uint64, ok bool) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	total, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	used, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return used, total, true
}
