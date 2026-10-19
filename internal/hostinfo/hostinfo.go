// Package hostinfo 通过 sshx.Runner 采集远端主机信息与 Agentless 指标。
//
// 契约见 docs/ARCHITECTURE.md 第 8.3 节（已冻结，签名不得改名）。
// 该包只依赖标准库与 pkg/protocol，不得 import internal/store 或 internal/api。
//
// 采集全部走 POSIX sh 命令：Linux 用 /proc 与 sysctl，macOS 没有 /proc，
// 因此走 sysctl / sw_vers / vm_stat / df 等。每条命令都做了失败兜底，
// 单项失败只跳过该字段，不影响其它指标。
package hostinfo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/pkg/protocol"
)

// Info 是主机静态信息，字段与 pkg/protocol.HostInfo 一一对应。
type Info struct {
	Hostname       string   `json:"hostname"`
	OS             string   `json:"os"` // linux | darwin | windows
	OSVersion      string   `json:"os_version"`
	Kernel         string   `json:"kernel"`
	Arch           string   `json:"arch"`
	CPUModel       string   `json:"cpu_model"`
	CPUCores       int      `json:"cpu_cores"`
	MemTotalBytes  uint64   `json:"mem_total_bytes"`
	DiskTotalBytes uint64   `json:"disk_total_bytes"`
	DiskUsedBytes  uint64   `json:"disk_used_bytes"`
	UptimeS        int64    `json:"uptime_s"`
	Timezone       string   `json:"timezone"`
	Docker         bool     `json:"docker"`
	DockerVersion  string   `json:"docker_version,omitempty"`
	IPAddrs        []string `json:"ip_addrs"`
	CollectedAt    int64    `json:"collected_at"` // Unix 毫秒
}

// collectScript 一次性输出所有字段（每个字段一行 "@@key@@value"）。
// 单个子命令失败只会让对应 value 为空，不会中断整个脚本。
const collectScript = `set +e
emit() { printf '@@%s@@%s\n' "$1" "$2"; }

os=$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')
emit hostname "$(uname -n 2>/dev/null)"
case "$os" in
  darwin|linux) : ;;
  *) os="" ;;
esac
emit os "$os"
emit kernel "$(uname -r 2>/dev/null)"

arch=$(uname -m 2>/dev/null)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  i386|i686) arch=386 ;;
esac
emit arch "$arch"

cpu_model=""
if [ "$os" = "darwin" ]; then
  cpu_model=$(sysctl -n machdep.cpu.brand_string 2>/dev/null)
  [ -z "$cpu_model" ] && cpu_model=$(sysctl -n hw.model 2>/dev/null)
else
  cpu_model=$(awk -F: '/^model name/{sub(/^[ \t]+/,"",$2); print $2; exit}' /proc/cpuinfo 2>/dev/null)
  if [ -z "$cpu_model" ]; then
    cpu_model=$(awk -F: '/^(Model|Hardware)[ \t]*:/{sub(/^[ \t]+/,"",$2); print $2; exit}' /proc/cpuinfo 2>/dev/null)
  fi
  [ -z "$cpu_model" ] && cpu_model=$(sysctl -n hw.model 2>/dev/null)
fi
emit cpu_model "$cpu_model"

cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null)
[ -z "$cores" ] && cores=$(sysctl -n hw.ncpu 2>/dev/null)
emit cpu_cores "$cores"

os_version=""
if [ "$os" = "darwin" ]; then
  os_name=$(sw_vers -productName 2>/dev/null)
  os_ver=$(sw_vers -productVersion 2>/dev/null)
  os_version="$os_name $os_ver"
else
  os_version=$(sh -c '. /etc/os-release 2>/dev/null; printf "%s" "${PRETTY_NAME:-$NAME}"' 2>/dev/null)
fi
emit os_version "$os_version"

mem_total=""
if [ "$os" = "darwin" ]; then
  mem_total=$(sysctl -n hw.memsize 2>/dev/null)
else
  mem_total=$(awk '/^MemTotal:/{printf "%d", $2*1024; exit}' /proc/meminfo 2>/dev/null)
fi
emit mem_total_bytes "$mem_total"

disk_total=""
disk_used=""
disk_fields=$(df -kP / 2>/dev/null | awk 'NR==2 {printf "%d %d", $2*1024, $3*1024}')
if [ -n "$disk_fields" ]; then
  set -- $disk_fields
  disk_total=$1
  disk_used=$2
fi
emit disk_total_bytes "$disk_total"
emit disk_used_bytes "$disk_used"

uptime_s=""
if [ "$os" = "darwin" ]; then
  boot_ts=$(sysctl -n kern.boottime 2>/dev/null | awk -F'[=,]' '{gsub(/ /,"",$2); print $2}')
  now_ts=$(date +%s 2>/dev/null)
  if [ -n "$boot_ts" ] && [ -n "$now_ts" ] && [ "$now_ts" -ge "$boot_ts" ] 2>/dev/null; then
    uptime_s=$((now_ts - boot_ts))
  fi
else
  uptime_s=$(awk '{printf "%d", $1}' /proc/uptime 2>/dev/null)
fi
emit uptime_s "$uptime_s"

tz=$(date +%Z 2>/dev/null)
[ -z "$tz" ] && tz=$(date +%z 2>/dev/null)
emit timezone "$tz"

docker=false
docker_version=""
if command -v docker >/dev/null 2>&1; then
  docker=true
  docker_version=$(docker version --format '{{.Server.Version}}' 2>/dev/null)
fi
emit docker "$docker"
emit docker_version "$docker_version"

ip_list=""
if [ "$os" = "darwin" ]; then
  ip_list=$(ifconfig -a 2>/dev/null | awk '/^[ \t]*inet /{print $2}' | grep -v '^127\.' 2>/dev/null)
else
  ip_list=$(ip -o -4 addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1)
  if [ -z "$ip_list" ]; then
    ip_list=$(ifconfig -a 2>/dev/null | awk '/^[ \t]*inet /{print $2}' | grep -v '^127\.' 2>/dev/null)
  fi
fi
ip_list=$(printf '%s\n' "$ip_list" | awk 'NF && !seen[$0]++ {printf "%s,", $0}')
ip_list=${ip_list%,}
emit ip_addrs "$ip_list"

exit 0
`

// Collect 通过 Runner 执行 POSIX 命令采集主机信息。
// 单项采集失败跳过该字段，而不是整体失败。
func Collect(ctx context.Context, r sshx.Runner) (Info, error) {
	if r == nil {
		return Info{}, errors.New("hostinfo: runner 不能为空")
	}
	stdout, _, exitCode, err := r.Run(ctx, collectScript)
	if err != nil {
		return Info{}, fmt.Errorf("hostinfo: 采集主机信息失败: %w", err)
	}

	info := parseInfo(stdout)
	info.CollectedAt = time.Now().UnixMilli()
	if info.IPAddrs == nil {
		info.IPAddrs = []string{}
	}

	// 完全没有可用字段：视为整体采集失败（例如远端没有 sh）。
	if info.Hostname == "" && info.OS == "" && info.Kernel == "" && info.Arch == "" {
		if exitCode != 0 {
			return Info{}, fmt.Errorf("hostinfo: 采集脚本退出码 %d，且没有可用输出", exitCode)
		}
		return Info{}, errors.New("hostinfo: 采集脚本没有返回可解析的数据")
	}
	return info, nil
}

func parseInfo(out string) Info {
	var info Info
	var ips []string

	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "@@"), "@@")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)

		switch key {
		case "hostname":
			info.Hostname = value
		case "os":
			info.OS = value
		case "os_version":
			info.OSVersion = value
		case "kernel":
			info.Kernel = value
		case "arch":
			info.Arch = value
		case "cpu_model":
			info.CPUModel = value
		case "cpu_cores":
			if v, err := strconv.Atoi(value); err == nil && v >= 0 {
				info.CPUCores = v
			}
		case "mem_total_bytes":
			if v, err := strconv.ParseUint(value, 10, 64); err == nil {
				info.MemTotalBytes = v
			}
		case "disk_total_bytes":
			if v, err := strconv.ParseUint(value, 10, 64); err == nil {
				info.DiskTotalBytes = v
			}
		case "disk_used_bytes":
			if v, err := strconv.ParseUint(value, 10, 64); err == nil {
				info.DiskUsedBytes = v
			}
		case "uptime_s":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil && v >= 0 {
				info.UptimeS = v
			}
		case "timezone":
			info.Timezone = value
		case "docker":
			info.Docker = value == "true"
		case "docker_version":
			info.DockerVersion = value
		case "ip_addrs":
			ips = append(ips, splitList(value)...)
		}
	}

	if len(ips) > 0 {
		seen := make(map[string]struct{}, len(ips))
		info.IPAddrs = make([]string, 0, len(ips))
		for _, ip := range ips {
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			info.IPAddrs = append(info.IPAddrs, ip)
		}
	}
	return info
}

// splitList 拆分逗号/空白分隔的列表，去空。
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ToProtocol 把 Info 转成 pkg/protocol.HostInfo（agent 上报 / REST 落库共用口径）。
func (i Info) ToProtocol() protocol.HostInfo {
	ips := i.IPAddrs
	if ips == nil {
		ips = []string{}
	}
	return protocol.HostInfo{
		Hostname:       i.Hostname,
		OS:             i.OS,
		OSVersion:      i.OSVersion,
		Kernel:         i.Kernel,
		Arch:           i.Arch,
		CPUModel:       i.CPUModel,
		CPUCores:       i.CPUCores,
		MemTotalBytes:  i.MemTotalBytes,
		DiskTotalBytes: i.DiskTotalBytes,
		DiskUsedBytes:  i.DiskUsedBytes,
		UptimeS:        i.UptimeS,
		Timezone:       i.Timezone,
		Docker:         i.Docker,
		DockerVersion:  i.DockerVersion,
		IPAddrs:        ips,
		CollectedAt:    i.CollectedAt,
	}
}
