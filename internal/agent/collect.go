package agent

import (
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/maliang/leadstack/pkg/protocol"
)

// Info 是节点静态信息，连接建立后通过 hello 上报一次。
type Info struct {
	Hostname   string
	OS         string
	Arch       string
	Kernel     string
	BootTimeMS int64
}

// Snapshot 是一次指标采集结果。
// 每个指标独立采集：失败时对应 HasXxx 为 false，该指标被跳过，不影响其他指标与心跳。
type Snapshot struct {
	TS int64

	CPUPct       float64
	HasCPU       bool
	MemUsedPct   float64
	MemUsedBytes float64
	HasMem       bool
	DiskUsedPct  float64
	HasDisk      bool
	Load1        float64
	HasLoad      bool
	UptimeS      int64
	HasUptime    bool
}

// Collector 封装 gopsutil 采集逻辑。
// 内部用互斥锁串行化采集调用，并用 500ms 的短期快照缓存合并心跳与采样循环的
// 并发调用：既避免同一瞬间重复采集，也避免 CPU 差值窗口过短导致数值抖动。
type Collector struct {
	mu   sync.Mutex
	log  *slog.Logger
	info Info

	lastSnap     Snapshot
	lastSnapAt   time.Time
	lastCPUTimes *cpu.TimesStat
}

// minSnapshotGap 是短期快照缓存的有效期；间隔内的重复调用直接复用上一份结果。
const minSnapshotGap = 500 * time.Millisecond

// NewCollector 创建采集器并缓存节点静态信息。
func NewCollector(log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	return &Collector{log: log, info: collectInfo(log)}
}

// Info 返回缓存的节点静态信息。
func (c *Collector) Info() Info {
	return c.info
}

// Snapshot 采集一次全部指标；任何单项失败都只跳过该项，不返回错误、不 panic。
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 心跳与指标循环可能几乎同时调用，短期内直接复用上次快照。
	if !c.lastSnapAt.IsZero() && time.Since(c.lastSnapAt) < minSnapshotGap {
		return c.lastSnap
	}

	snap := Snapshot{TS: nowMillis()}

	// CPU 使用率：用两次 cpu.Times 的差值计算，首次调用回退为开机以来平均值。
	c.safeCollect("cpu", func() {
		if pct, ok := c.cpuPercentLocked(); ok {
			snap.CPUPct = pct
			snap.HasCPU = true
		}
	})

	// 内存使用率与已用字节。
	c.safeCollect("mem", func() {
		vm, err := mem.VirtualMemory()
		if err != nil {
			c.log.Debug("采集内存使用率失败，跳过", "err", err)
			return
		}
		if vm != nil {
			snap.MemUsedPct = vm.UsedPercent
			snap.MemUsedBytes = float64(vm.Used)
			snap.HasMem = true
		}
	})

	// 根分区磁盘使用率（Windows 取系统盘，见 rootPath 的平台实现）。
	c.safeCollect("disk", func() {
		usage, err := disk.Usage(rootPath())
		if err != nil {
			c.log.Debug("采集磁盘使用率失败，跳过", "err", err, "path", rootPath())
			return
		}
		if usage != nil {
			snap.DiskUsedPct = usage.UsedPercent
			snap.HasDisk = true
		}
	})

	// 1 分钟平均负载（部分平台不支持，失败即跳过）。
	c.safeCollect("load", func() {
		avg, err := load.Avg()
		if err != nil {
			c.log.Debug("采集系统负载失败，跳过", "err", err)
			return
		}
		if avg != nil {
			snap.Load1 = avg.Load1
			snap.HasLoad = true
		}
	})

	// 系统运行时长。
	c.safeCollect("uptime", func() {
		up, err := host.Uptime()
		if err != nil {
			c.log.Debug("采集运行时长失败，跳过", "err", err)
			return
		}
		snap.UptimeS = int64(up)
		snap.HasUptime = true
	})

	c.lastSnap = snap
	c.lastSnapAt = time.Now()
	return snap
}

// safeCollect 执行单项采集并兜底 recover：
// 即使某个平台的采集实现意外 panic，也只丢弃该项指标，不影响心跳与其他指标。
func (c *Collector) safeCollect(metric string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Warn("采集指标时发生 panic，已跳过该指标", "metric", metric, "panic", r)
		}
	}()
	fn()
}

// cpuPercentLocked 计算 CPU 使用率：
// 有上一次 cpu.Times 基线时用差值计算；首次调用（无基线）回退为开机以来的平均使用率。
// 调用方必须持有 c.mu。
func (c *Collector) cpuPercentLocked() (float64, bool) {
	times, err := cpu.Times(false)
	if err != nil {
		c.log.Debug("采集 CPU 时间失败，跳过", "err", err)
		return 0, false
	}
	if len(times) == 0 {
		return 0, false
	}
	cur := times[0]
	curTotal := cpuTotal(cur)
	if curTotal <= 0 {
		return 0, false
	}
	idle := cur.Idle + cur.Iowait

	if prev := c.lastCPUTimes; prev != nil {
		prevTotal := cpuTotal(*prev)
		prevIdle := prev.Idle + prev.Iowait
		c.lastCPUTimes = &cur
		deltaTotal := curTotal - prevTotal
		if deltaTotal > 0 {
			return clampPercent((deltaTotal - (idle - prevIdle)) / deltaTotal * 100), true
		}
		// 时间戳未推进（极端情况），退回开机以来平均值。
		return clampPercent((curTotal - idle) / curTotal * 100), true
	}

	c.lastCPUTimes = &cur
	return clampPercent((curTotal - idle) / curTotal * 100), true
}

// cpuTotal 汇总一个 CPU 时间样本的忙碌与空闲总时间。
// Guest/GuestNice 在 Linux 上已包含在 User/Nice 中，不重复累加。
func cpuTotal(t cpu.TimesStat) float64 {
	return t.User + t.System + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal + t.Idle
}

// clampPercent 把百分比限制在 [0, 100]。
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// Heartbeat 把采集结果转换成心跳负载；缺失的指标填 0，保证心跳始终可发。
func (s Snapshot) Heartbeat() protocol.Heartbeat {
	return protocol.Heartbeat{
		CPUPct:     s.CPUPct,
		MemUsedPct: s.MemUsedPct,
		DiskUsedPt: s.DiskUsedPct,
		Load1:      s.Load1,
		UptimeS:    s.UptimeS,
	}
}

// Samples 把采集结果转换成指标样本；只包含采集成功的指标。
func (s Snapshot) Samples() []protocol.Sample {
	samples := make([]protocol.Sample, 0, 5)
	if s.HasCPU {
		samples = append(samples, protocol.Sample{Metric: protocol.MetricCPUPct, Value: s.CPUPct, TS: s.TS})
	}
	if s.HasMem {
		samples = append(samples,
			protocol.Sample{Metric: protocol.MetricMemUsedPct, Value: s.MemUsedPct, TS: s.TS},
			protocol.Sample{Metric: protocol.MetricMemUsedB, Value: s.MemUsedBytes, TS: s.TS},
		)
	}
	if s.HasLoad {
		samples = append(samples, protocol.Sample{Metric: protocol.MetricLoad1, Value: s.Load1, TS: s.TS})
	}
	if s.HasDisk {
		samples = append(samples, protocol.Sample{Metric: protocol.MetricDiskUsedPt, Value: s.DiskUsedPct, TS: s.TS})
	}
	return samples
}

// collectInfo 采集节点静态信息；任何字段失败都回退到 Go 运行时提供的安全默认值。
func collectInfo(log *slog.Logger) Info {
	info := Info{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	if hostname, err := os.Hostname(); err != nil {
		log.Debug("获取主机名失败", "err", err)
	} else {
		info.Hostname = hostname
	}
	if hi, err := host.Info(); err != nil {
		log.Debug("获取主机信息失败，使用运行时默认值", "err", err)
		return info
	} else {
		if hi.Hostname != "" {
			info.Hostname = hi.Hostname
		}
		if hi.OS != "" {
			info.OS = hi.OS
		}
		info.Kernel = hi.KernelVersion
		// gopsutil 的 BootTime 单位是秒，协议要求 Unix 毫秒。
		if hi.BootTime > 0 {
			info.BootTimeMS = int64(hi.BootTime) * int64(time.Second/time.Millisecond)
		}
	}
	return info
}
