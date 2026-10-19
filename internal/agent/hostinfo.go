package agent

import (
	"context"
	"fmt"
	"log/slog"
	netstd "net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/maliang/leadstack/pkg/protocol"
)

const (
	// hostInfoRetryDelay 主机信息上报失败后重试一次的等待时间。
	hostInfoRetryDelay = time.Second
	// dockerProbeTimeout Docker 探测超时，避免 docker CLI 卡住拖慢采集。
	dockerProbeTimeout = 3 * time.Second
)

// hostInfoInterval 是主机信息周期上报间隔：连接成功后立即一次，之后每 6 小时一次。
// 定义为变量而不是常量，便于测试覆盖。
var hostInfoInterval = 6 * time.Hour

// dockerDetect 探测 Docker 是否可用及服务端版本。
// 定义为变量，便于测试替换为确定结果。
var dockerDetect = detectDocker

// HostInfo 采集一次主机静态信息（协议 HostInfo 载荷）。
//
// 各字段独立采集：任何一项失败只留空该字段，不影响其它字段；
// 整个过程有 recover 兜底，绝不 panic，也不会返回错误。
func (c *Collector) HostInfo() (info protocol.HostInfo) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Warn("采集主机信息时发生 panic，已返回已采集到的字段", "panic", r)
		}
	}()

	// 先用缓存的静态信息兜底，保证即使 gopsutil 全部失败也有基本字段。
	info = protocol.HostInfo{
		Hostname:    c.info.Hostname,
		OS:          c.info.OS,
		Arch:        c.info.Arch,
		Kernel:      c.info.Kernel,
		CPUCores:    runtime.NumCPU(),
		CollectedAt: nowMillis(),
	}

	// Docker 探测可能阻塞数秒，放在采集锁之外，避免影响心跳/指标采集。
	info.Docker, info.DockerVersion = dockerDetect(c.log)

	c.mu.Lock()
	defer c.mu.Unlock()

	// 主机信息：操作系统版本、内核、运行时长。
	if hi, err := host.Info(); err != nil {
		c.log.Debug("采集主机信息失败，使用默认值", "err", err)
	} else if hi != nil {
		if info.Hostname == "" {
			info.Hostname = hi.Hostname
		}
		if info.OS == "" {
			info.OS = hi.OS
		}
		if hi.KernelVersion != "" {
			info.Kernel = hi.KernelVersion
		}
		info.OSVersion = hi.PlatformVersion
		info.UptimeS = int64(hi.Uptime)
	}
	if info.UptimeS <= 0 {
		if up, err := host.Uptime(); err == nil {
			info.UptimeS = int64(up)
		}
	}

	// CPU 型号（取第一颗的型号名；核数用逻辑核数，与 agentless 采集口径一致）。
	if infos, err := cpu.Info(); err != nil {
		c.log.Debug("采集 CPU 型号失败，跳过", "err", err)
	} else if len(infos) > 0 {
		info.CPUModel = strings.TrimSpace(infos[0].ModelName)
	}

	// 内存总量。
	if vm, err := mem.VirtualMemory(); err != nil {
		c.log.Debug("采集内存总量失败，跳过", "err", err)
	} else if vm != nil {
		info.MemTotalBytes = vm.Total
	}

	// 根分区总量与已用量（与 agentless 采集同样使用根分区口径）。
	if usage, err := disk.Usage(rootPath()); err != nil {
		c.log.Debug("采集磁盘信息失败，跳过", "err", err, "path", rootPath())
	} else if usage != nil {
		info.DiskTotalBytes = usage.Total
		info.DiskUsedBytes = usage.Used
	}

	info.Timezone = detectTimezone()
	info.IPAddrs = collectIPAddrs(c.log)

	return info
}

// detectDocker 探测 Docker：优先用 `docker version --format` 拿服务端版本，
// CLI 不可用时回退为检测 docker socket 是否存在。
func detectDocker(log *slog.Logger) (available bool, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err == nil {
		return true, strings.TrimSpace(string(out))
	}
	if dockerSocketExists() {
		log.Debug("docker CLI 不可用，但检测到 docker socket", "err", err)
		return true, ""
	}
	log.Debug("未检测到 Docker", "err", err)
	return false, ""
}

// detectTimezone 推断 IANA 时区名：
// TZ 环境变量 → /etc/localtime 符号链接 → Go 本地时区 → 时区缩写兜底（如 Windows）。
func detectTimezone() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		const marker = "zoneinfo/"
		if idx := strings.LastIndex(link, marker); idx >= 0 {
			return link[idx+len(marker):]
		}
	}
	if name := time.Now().Location().String(); name != "" && name != "Local" {
		return name
	}
	if name, _ := time.Now().Zone(); name != "" {
		return name
	}
	return ""
}

// collectIPAddrs 收集非回环、非链路本地的本机 IP 地址（IPv4 与 IPv6 均保留），去重并排序。
func collectIPAddrs(log *slog.Logger) []string {
	ifaces, err := netstd.Interfaces()
	if err != nil {
		log.Debug("枚举网卡失败，跳过 IP 列表", "err", err)
		return nil
	}
	seen := make(map[string]struct{})
	for _, iface := range ifaces {
		if iface.Flags&netstd.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip netstd.IP
			switch v := addr.(type) {
			case *netstd.IPNet:
				ip = v.IP
			case *netstd.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
				continue
			}
			seen[ip.String()] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	addrs := make([]string, 0, len(seen))
	for ip := range seen {
		addrs = append(addrs, ip)
	}
	sort.Strings(addrs)
	return addrs
}

// hostInfoLoop 在收到 hello_ack 后立即上报一次主机信息，之后每 hostInfoInterval 上报一次。
// 上报失败不影响心跳，也不退出循环（连接断开由 ctx / closed 感知）。
func (a *Agent) hostInfoLoop(ctx context.Context, w *wsConn, ready <-chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-w.closed:
		return
	case <-ready:
	}

	if err := a.sendHostInfo(ctx, w); err != nil {
		a.log.Warn("首次上报主机信息失败", "err", err)
	}

	for {
		timer := time.NewTimer(hostInfoInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.closed:
			timer.Stop()
			return
		case <-timer.C:
			if err := a.sendHostInfo(ctx, w); err != nil {
				a.log.Warn("周期上报主机信息失败，等待下一周期", "err", err)
			}
		}
	}
}

// sendHostInfo 采集并上报一次主机信息；发送失败时重试一次，仍失败则返回错误。
func (a *Agent) sendHostInfo(ctx context.Context, w *wsConn) error {
	info := a.col.HostInfo()
	env := newEnvelope(protocol.TypeHostInfo, info)

	if err := w.send(ctx, env); err != nil {
		a.log.Warn("上报主机信息失败，1 秒后重试一次", "err", err)
		if !sleepCtx(ctx, hostInfoRetryDelay) {
			return ctx.Err()
		}
		if err := w.send(ctx, env); err != nil {
			return fmt.Errorf("重试后仍失败: %w", err)
		}
		a.log.Info("主机信息重试上报成功")
		return nil
	}

	a.log.Info("已上报主机信息",
		"hostname", info.Hostname,
		"os", info.OS,
		"os_version", info.OSVersion,
		"kernel", info.Kernel,
		"arch", info.Arch,
		"cpu_cores", info.CPUCores,
		"docker", info.Docker)
	return nil
}
