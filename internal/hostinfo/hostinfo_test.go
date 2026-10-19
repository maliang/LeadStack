package hostinfo

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/devssh"
	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/pkg/protocol"
)

// fakeRunner 是可编排的 sshx.Runner 测试替身。
type fakeRunner struct {
	mu      sync.Mutex
	calls   []string
	handler func(cmd string) (stdout, stderr string, exitCode int, err error)
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (string, string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	return f.handler(cmd)
}

const fullCollectOutput = `@@hostname@@devbox
@@os@@linux
@@os_version@@Ubuntu 24.04.1 LTS
@@kernel@@6.8.0-45-generic
@@arch@@amd64
@@cpu_model@@Intel(R) Xeon(R) CPU E5-2670 0 @ 2.60GHz
@@cpu_cores@@8
@@mem_total_bytes@@17179869184
@@disk_total_bytes@@107374182400
@@disk_used_bytes@@53687091200
@@uptime_s@@123456
@@timezone@@CST
@@docker@@true
@@docker_version@@27.1.0
@@ip_addrs@@10.0.0.5,10.0.0.6,10.0.0.5
`

func TestCollectParsesAllFields(t *testing.T) {
	r := &fakeRunner{handler: func(string) (string, string, int, error) {
		return fullCollectOutput, "", 0, nil
	}}
	info, err := Collect(context.Background(), r)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if info.Hostname != "devbox" || info.OS != "linux" || info.OSVersion != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("基础字段解析错误: %+v", info)
	}
	if info.Kernel != "6.8.0-45-generic" || info.Arch != "amd64" {
		t.Fatalf("内核/架构解析错误: %+v", info)
	}
	if info.CPUModel != "Intel(R) Xeon(R) CPU E5-2670 0 @ 2.60GHz" || info.CPUCores != 8 {
		t.Fatalf("CPU 解析错误: %+v", info)
	}
	if info.MemTotalBytes != 17179869184 {
		t.Fatalf("内存解析错误: %d", info.MemTotalBytes)
	}
	if info.DiskTotalBytes != 107374182400 || info.DiskUsedBytes != 53687091200 {
		t.Fatalf("磁盘解析错误: %+v", info)
	}
	if info.UptimeS != 123456 || info.Timezone != "CST" {
		t.Fatalf("uptime/时区解析错误: %+v", info)
	}
	if !info.Docker || info.DockerVersion != "27.1.0" {
		t.Fatalf("Docker 解析错误: %+v", info)
	}
	wantIPs := []string{"10.0.0.5", "10.0.0.6"}
	if len(info.IPAddrs) != len(wantIPs) || info.IPAddrs[0] != wantIPs[0] || info.IPAddrs[1] != wantIPs[1] {
		t.Fatalf("IP 去重解析错误: %v", info.IPAddrs)
	}
	if info.CollectedAt == 0 {
		t.Fatal("CollectedAt 应被填充")
	}
	if delta := time.Since(time.UnixMilli(info.CollectedAt)); delta < 0 || delta > time.Minute {
		t.Fatalf("CollectedAt 不在合理范围: %v", delta)
	}
}

func TestCollectSkipsMissingFields(t *testing.T) {
	// 模拟“单条命令失败”：大部分字段为空，但采集整体仍成功。
	out := "@@hostname@@partial\n@@os@@darwin\n@@kernel@@\n@@mem_total_bytes@@\n@@docker@@false\n"
	r := &fakeRunner{handler: func(string) (string, string, int, error) {
		return out, "some stderr", 0, nil
	}}
	info, err := Collect(context.Background(), r)
	if err != nil {
		t.Fatalf("部分字段缺失不应整体报错: %v", err)
	}
	if info.Hostname != "partial" || info.OS != "darwin" {
		t.Fatalf("已采集字段丢失: %+v", info)
	}
	if info.Kernel != "" || info.CPUCores != 0 || info.MemTotalBytes != 0 || info.UptimeS != 0 {
		t.Fatalf("缺失字段应为零值: %+v", info)
	}
	if info.Docker {
		t.Fatal("docker=false 解析错误")
	}
	if info.IPAddrs == nil || len(info.IPAddrs) != 0 {
		t.Fatalf("IPAddrs 应为非 nil 空切片: %#v", info.IPAddrs)
	}
}

func TestCollectFailures(t *testing.T) {
	t.Run("传输层失败", func(t *testing.T) {
		r := &fakeRunner{handler: func(string) (string, string, int, error) {
			return "", "", -1, errors.New("connection reset")
		}}
		if _, err := Collect(context.Background(), r); err == nil {
			t.Fatal("传输层失败必须返回 error")
		}
	})

	t.Run("空输出且非零退出", func(t *testing.T) {
		r := &fakeRunner{handler: func(string) (string, string, int, error) {
			return "", "", 127, nil
		}}
		if _, err := Collect(context.Background(), r); err == nil {
			t.Fatal("空输出必须返回 error")
		}
	})

	t.Run("nil runner", func(t *testing.T) {
		if _, err := Collect(context.Background(), nil); err == nil {
			t.Fatal("nil runner 必须返回 error")
		}
	})
}

func TestParseInfoHandlesCRLFAndGarbage(t *testing.T) {
	out := "garbage line\r\n@@hostname@@box\r\n@@os@@linux\r\n@@cpu_cores@@notanumber\r\n@@docker@@1\r\n"
	info := parseInfo(out)
	if info.Hostname != "box" || info.OS != "linux" {
		t.Fatalf("CRLF 解析错误: %+v", info)
	}
	if info.CPUCores != 0 {
		t.Fatalf("非法整数应被忽略: %d", info.CPUCores)
	}
	if info.Docker {
		t.Fatal("docker 非 true 时应为 false")
	}
	if info.IPAddrs != nil {
		t.Fatalf("无 IP 时 IPAddrs 应为 nil: %#v", info.IPAddrs)
	}
}

func TestToProtocolJSONTagsMatch(t *testing.T) {
	info := Info{Hostname: "h", OS: "linux", IPAddrs: []string{"1.2.3.4"}, CollectedAt: 1}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal Info: %v", err)
	}
	var infoMap map[string]any
	if err := json.Unmarshal(b, &infoMap); err != nil {
		t.Fatalf("Unmarshal Info: %v", err)
	}

	pb, err := json.Marshal(info.ToProtocol())
	if err != nil {
		t.Fatalf("Marshal HostInfo: %v", err)
	}
	var protoMap map[string]any
	if err := json.Unmarshal(pb, &protoMap); err != nil {
		t.Fatalf("Unmarshal HostInfo: %v", err)
	}

	for key := range infoMap {
		if _, ok := protoMap[key]; !ok {
			t.Fatalf("protocol.HostInfo 缺少 json 字段 %q", key)
		}
	}
	for key := range protoMap {
		if _, ok := infoMap[key]; !ok {
			t.Fatalf("hostinfo.Info 缺少 json 字段 %q", key)
		}
	}
}

// scriptedSamplerRunner 按命令返回预设输出；parse 失败的命令返回 error。
func scriptedSamplerRunner(t *testing.T, responses map[string][]string) *fakeRunner {
	t.Helper()
	mu := sync.Mutex{}
	idx := map[string]int{}
	return &fakeRunner{handler: func(cmd string) (string, string, int, error) {
		vals, ok := responses[cmd]
		if !ok {
			// 未编排的命令视为“该单项输出为空”，只影响对应指标。
			return "", "", 0, nil
		}
		mu.Lock()
		defer mu.Unlock()
		i := idx[cmd]
		if i >= len(vals) {
			i = len(vals) - 1
		}
		idx[cmd] = i + 1
		return vals[i], "", 0, nil
	}}
}

func sampleValue(t *testing.T, samples []protocol.Sample, metric string) (float64, bool) {
	t.Helper()
	for _, s := range samples {
		if s.Metric == metric {
			return s.Value, true
		}
	}
	return 0, false
}

func linuxSamplerResponses() map[string][]string {
	return map[string][]string{
		cpuCommand: {
			"linux 100 0 50 850 0 0 0",   // total 1000, idle 850
			"linux 200 0 100 1700 0 0 0", // total 2000, idle 1700 -> busy 15%
			"linux 300 0 150 2550 0 0 0", // total 3000, idle 2550 -> busy 15%
		},
		memCommand:    {"17179869184 8589934592"},
		loadCommand:   {"0.75"},
		diskCommand:   {"42.50"},
		uptimeCommand: {"98765"},
	}
}

func TestSamplerLinuxCPUInterval(t *testing.T) {
	r := scriptedSamplerRunner(t, linuxSamplerResponses())
	s := NewSampler()

	first, err := s.Collect(context.Background(), "srv-1", r)
	if err != nil {
		t.Fatalf("首帧 Collect: %v", err)
	}
	if _, ok := sampleValue(t, first, protocol.MetricCPUPct); ok {
		t.Fatal("首帧没有上一份 CPU 计数，不应返回 cpu.pct")
	}
	if v, ok := sampleValue(t, first, protocol.MetricMemUsedB); !ok || v != 8589934592 {
		t.Fatalf("mem.used_bytes 错误: %v %v", v, ok)
	}
	if v, ok := sampleValue(t, first, protocol.MetricMemUsedPct); !ok || v < 49.99 || v > 50.01 {
		t.Fatalf("mem.used_pct 错误: %v %v", v, ok)
	}
	if v, ok := sampleValue(t, first, protocol.MetricLoad1); !ok || v != 0.75 {
		t.Fatalf("load.1 错误: %v %v", v, ok)
	}
	if v, ok := sampleValue(t, first, protocol.MetricDiskUsedPt); !ok || v != 42.5 {
		t.Fatalf("disk.used.pct 错误: %v %v", v, ok)
	}
	if v, ok := sampleValue(t, first, metricUptimeS); !ok || v != 98765 {
		t.Fatalf("uptime_s 错误: %v %v", v, ok)
	}

	second, err := s.Collect(context.Background(), "srv-1", r)
	if err != nil {
		t.Fatalf("第二帧 Collect: %v", err)
	}
	pct, ok := sampleValue(t, second, protocol.MetricCPUPct)
	if !ok {
		t.Fatal("第二帧应返回 cpu.pct")
	}
	if pct < 14.99 || pct > 15.01 {
		t.Fatalf("区间 CPU 占用率 = %v, 期望 15", pct)
	}

	// 所有 sample 的 ts 应一致。
	for _, sample := range second {
		if sample.TS == 0 {
			t.Fatalf("sample %s 缺少 ts", sample.Metric)
		}
	}
}

func TestSamplerIsolatesServers(t *testing.T) {
	r := scriptedSamplerRunner(t, linuxSamplerResponses())
	s := NewSampler()

	// srv-A 先建立基线。
	if _, err := s.Collect(context.Background(), "srv-A", r); err != nil {
		t.Fatalf("srv-A 首帧: %v", err)
	}
	// srv-B 首帧：尽管 srv-A 已有基线，B 仍应视为首帧。
	samplesB, err := s.Collect(context.Background(), "srv-B", r)
	if err != nil {
		t.Fatalf("srv-B 首帧: %v", err)
	}
	if _, ok := sampleValue(t, samplesB, protocol.MetricCPUPct); ok {
		t.Fatal("不同 serverID 的 CPU 基线必须隔离")
	}
}

func TestSamplerStaleBaseline(t *testing.T) {
	s := NewSampler()
	s.last["srv"] = cpuSnapshot{total: 1000, idle: 850, at: time.Now().Add(-10 * time.Minute)}
	responses := linuxSamplerResponses()
	responses[cpuCommand] = []string{"linux 200 0 100 1700 0 0 0"}
	r := scriptedSamplerRunner(t, responses)

	samples, err := s.Collect(context.Background(), "srv", r)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, ok := sampleValue(t, samples, protocol.MetricCPUPct); ok {
		t.Fatal("跨采集窗口无样本时应跳过 cpu.pct")
	}
	// 基线应更新为新快照，供下一次使用。
	s.mu.Lock()
	snap := s.last["srv"]
	s.mu.Unlock()
	if snap.total != 2000 || snap.idle != 1700 {
		t.Fatalf("过期基线未刷新: %+v", snap)
	}
}

func TestSamplerCounterReset(t *testing.T) {
	s := NewSampler()
	s.last["srv"] = cpuSnapshot{total: 5000, idle: 4000, at: time.Now().Add(-30 * time.Second)}
	responses := linuxSamplerResponses()
	responses[cpuCommand] = []string{"linux 200 0 100 1700 0 0 0", "linux 300 0 150 2550 0 0 0"}
	r := scriptedSamplerRunner(t, responses)

	samples, err := s.Collect(context.Background(), "srv", r)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, ok := sampleValue(t, samples, protocol.MetricCPUPct); ok {
		t.Fatal("CPU 计数器回退时应跳过 cpu.pct")
	}

	// 下一帧以回退后的计数为新基线，应能算出正常区间占用率。
	samples, err = s.Collect(context.Background(), "srv", r)
	if err != nil {
		t.Fatalf("第二帧 Collect: %v", err)
	}
	pct, ok := sampleValue(t, samples, protocol.MetricCPUPct)
	if !ok || pct < 14.99 || pct > 15.01 {
		t.Fatalf("计数器回退后第二帧 cpu.pct = %v (ok=%v)", pct, ok)
	}
}

func TestSamplerZeroDelta(t *testing.T) {
	responses := linuxSamplerResponses()
	responses[cpuCommand] = []string{"linux 100 0 50 850 0 0 0", "linux 100 0 50 850 0 0 0"}
	s := NewSampler()
	r := scriptedSamplerRunner(t, responses)

	if _, err := s.Collect(context.Background(), "srv", r); err != nil {
		t.Fatalf("首帧: %v", err)
	}
	samples, err := s.Collect(context.Background(), "srv", r)
	if err != nil {
		t.Fatalf("第二帧: %v", err)
	}
	if _, ok := sampleValue(t, samples, protocol.MetricCPUPct); ok {
		t.Fatal("deltaTotal 为 0 时应跳过 cpu.pct")
	}
}

func TestSamplerDarwinPS(t *testing.T) {
	r := scriptedSamplerRunner(t, map[string][]string{
		cpuCommand: {"ps 250.00 8"},
	})
	s := NewSampler()
	samples, err := s.Collect(context.Background(), "mac", r)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	pct, ok := sampleValue(t, samples, protocol.MetricCPUPct)
	if !ok {
		t.Fatal("macOS ps 路径应直接返回 cpu.pct")
	}
	if pct < 31.24 || pct > 31.26 {
		t.Fatalf("ps 归一化 CPU%% = %v, 期望 31.25", pct)
	}
}

func TestSamplerPartialFailure(t *testing.T) {
	r := &fakeRunner{handler: func(cmd string) (string, string, int, error) {
		switch cmd {
		case cpuCommand:
			return "", "", -1, errors.New("cpu 命令失败")
		case memCommand:
			return "1000 250", "", 0, nil
		case loadCommand:
			return "0.5", "", 0, nil
		case diskCommand:
			return "10.00", "", 0, nil
		case uptimeCommand:
			return "100", "", 0, nil
		default:
			return "", "", 0, errors.New("unknown")
		}
	}}
	s := NewSampler()
	samples, err := s.Collect(context.Background(), "srv", r)
	if err != nil {
		t.Fatalf("单项失败不应整体报错: %v", err)
	}
	if _, ok := sampleValue(t, samples, protocol.MetricCPUPct); ok {
		t.Fatal("cpu 命令失败时不应有 cpu.pct")
	}
	if _, ok := sampleValue(t, samples, protocol.MetricMemUsedPct); !ok {
		t.Fatal("其它指标应照常返回")
	}
}

func TestSamplerAllFail(t *testing.T) {
	r := &fakeRunner{handler: func(string) (string, string, int, error) {
		return "", "", -1, errors.New("ssh down")
	}}
	s := NewSampler()
	if _, err := s.Collect(context.Background(), "srv", r); err == nil {
		t.Fatal("所有指标都失败时应返回 error")
	}
}

// TestCollectAgainstDevssh 用 devssh 起真实 SSH 会话，端到端验证主机信息与指标采集。
func TestCollectAgainstDevssh(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("devssh 集成测试仅支持 linux/darwin")
	}

	srv, err := devssh.Start(devssh.Config{})
	if err != nil {
		t.Fatalf("启动 devssh: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	host, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("拆分 devssh 地址: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析端口: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := sshx.Dial(ctx, sshx.DialOptions{
		Host:          host,
		Port:          port,
		Auth:          sshx.Auth{Username: "devuser", Password: "devpass"},
		HostKeyPolicy: sshx.PolicyInsecure,
	})
	if err != nil {
		t.Fatalf("Dial devssh: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	info, err := Collect(ctx, client)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if info.Hostname == "" {
		t.Fatal("hostname 为空")
	}
	if info.OS != runtime.GOOS {
		t.Fatalf("os = %q, 期望 %q", info.OS, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Fatalf("arch = %q, 期望 %q", info.Arch, runtime.GOARCH)
	}
	if info.Kernel == "" || info.CPUModel == "" || info.OSVersion == "" {
		t.Fatalf("静态字段缺失: %+v", info)
	}
	if info.CPUCores <= 0 || info.MemTotalBytes == 0 || info.DiskTotalBytes == 0 {
		t.Fatalf("容量字段异常: %+v", info)
	}
	if info.DiskUsedBytes > info.DiskTotalBytes {
		t.Fatalf("磁盘已用 > 总量: %+v", info)
	}
	if info.UptimeS <= 0 || info.Timezone == "" {
		t.Fatalf("uptime/时区异常: %+v", info)
	}
	if info.CollectedAt == 0 {
		t.Fatal("CollectedAt 为空")
	}

	// 连续两次采样：第二次必须能给出 CPU 区间占用率。
	sampler := NewSampler()
	if _, err := sampler.Collect(ctx, "devssh", client); err != nil {
		t.Fatalf("首次指标采集: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	samples, err := sampler.Collect(ctx, "devssh", client)
	if err != nil {
		t.Fatalf("第二次指标采集: %v", err)
	}
	required := []string{protocol.MetricCPUPct, protocol.MetricMemUsedPct, protocol.MetricMemUsedB, protocol.MetricLoad1, protocol.MetricDiskUsedPt, metricUptimeS}
	for _, metric := range required {
		if _, ok := sampleValue(t, samples, metric); !ok {
			t.Fatalf("第二次采样缺少指标 %s: %+v", metric, samples)
		}
	}
	if pct, _ := sampleValue(t, samples, protocol.MetricCPUPct); pct < 0 || pct > 100 {
		t.Fatalf("cpu.pct 越界: %v", pct)
	}
	for _, sample := range samples {
		if strings.TrimSpace(sample.Metric) == "" || sample.TS == 0 {
			t.Fatalf("非法 sample: %+v", sample)
		}
	}
}
