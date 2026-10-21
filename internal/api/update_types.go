package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 升级相关文件名（契约 §51）。
const (
	upgradeStateFileName   = "state.json"
	upgradeHistoryFileName = "upgrade-history.jsonl"
	upgradeChecksumsName   = "checksums.txt"
	upgradePrevSuffix      = ".prev"
	upgradeFailedPrefix    = ".failed-"
	// upgradeNotesMaxBytes 是 release notes 的截断上限（4KB）。
	upgradeNotesMaxBytes = 4096
	// upgradeSelfCheckTimeout 是新进程自检的最长等待时间。
	upgradeSelfCheckTimeout = 10 * time.Second
)

// UpdateAsset 是一个发行产物。
type UpdateAsset struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
	Name      string `json:"name,omitempty"`
}

// UpdateInfo 是检查更新的结果（契约 51.2）。
type UpdateInfo struct {
	Current       string        `json:"current"`
	Latest        string        `json:"latest"`
	HasUpdate     bool          `json:"has_update"`
	PublishedAt   int64         `json:"published_at"`
	ReleaseURL    string        `json:"release_url"`
	Notes         string        `json:"notes"`
	SchemaChanged bool          `json:"schema_changed"`
	Assets        []UpdateAsset `json:"assets"`
	Upgradable    bool          `json:"upgradable"`
	CheckedAt     int64         `json:"checked_at"`
	Error         string        `json:"error"`
}

// UpgradeState 是 <data_dir>/upgrade/state.json 的内容（watchdog 与自检共用）。
type UpgradeState struct {
	TargetVersion string   `json:"target_version"`
	FromVersion   string   `json:"from_version"`
	PrevExe       string   `json:"prev_exe"`
	NewExe        string   `json:"new_exe"`
	BackupFile    string   `json:"backup_file"`
	SchemaChanged bool     `json:"schema_changed"`
	StartedAt     int64    `json:"started_at"`
	Deadline      int64    `json:"deadline"`
	WatchdogPID   int      `json:"watchdog_pid"`
	Args          []string `json:"args"`
	DataDir       string   `json:"data_dir"`
	// ActiveListen 是升级前正在监听的地址（watchdog 用它做「新进程没起来」的早期判定）。
	ActiveListen string `json:"active_listen"`
}

// UpgradeHistoryEntry 是一条升级历史（成功或回退）。
type UpgradeHistoryEntry struct {
	TS            int64    `json:"ts"`
	Outcome       string   `json:"outcome"` // success | rollback
	FromVersion   string   `json:"from_version"`
	TargetVersion string   `json:"target_version"`
	BackupFile    string   `json:"backup_file,omitempty"`
	RestoredFiles []string `json:"restored_files,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	PrevExe       string   `json:"prev_exe,omitempty"`
	DurationMS    int64    `json:"duration_ms,omitempty"`
}

// UpgradeStatePathFor 返回某个数据目录下的升级状态文件路径（cmd 启动自检用）。
func UpgradeStatePathFor(dataDir string) string { return upgradeStatePath(dataDir) }

// upgradeStatePath 返回 state.json 路径。
func upgradeStatePath(dataDir string) string {
	return filepath.Join(dataDir, "upgrade", upgradeStateFileName)
}

// upgradeHistoryPath 返回升级历史文件路径。
func upgradeHistoryPath(dataDir string) string {
	return filepath.Join(dataDir, "upgrade", upgradeHistoryFileName)
}

// ReadUpgradeState 读取 state.json（不存在返回 nil, nil）。
func ReadUpgradeState(path string) (*UpgradeState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取升级状态 %s: %w", path, err)
	}
	var state UpgradeState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("解析升级状态 %s: %w", path, err)
	}
	return &state, nil
}

// WriteUpgradeState 原子写入 state.json。
func WriteUpgradeState(path string, state *UpgradeState) error {
	if state == nil {
		return errors.New("升级状态为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("创建升级目录: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化升级状态: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入升级状态: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("落盘升级状态: %w", err)
	}
	return nil
}

// RemoveUpgradeState 删除 state.json（新进程自检成功后调用）。
func RemoveUpgradeState(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除升级状态 %s: %w", path, err)
	}
	return nil
}

// AppendUpgradeHistory 追加一条升级历史。
func AppendUpgradeHistory(dataDir string, entry *UpgradeHistoryEntry) error {
	if entry == nil {
		return errors.New("升级历史为空")
	}
	path := upgradeHistoryPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("创建升级目录: %w", err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化升级历史: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("打开升级历史 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("写入升级历史: %w", err)
	}
	return nil
}

// ReadUpgradeHistory 读取最近的升级历史（最多 limit 条，倒序）。
func ReadUpgradeHistory(dataDir string, limit int) []UpgradeHistoryEntry {
	if limit <= 0 {
		limit = 20
	}
	raw, err := os.ReadFile(upgradeHistoryPath(dataDir))
	if err != nil {
		return []UpgradeHistoryEntry{}
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	out := make([]UpgradeHistoryEntry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var entry UpgradeHistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Semver 是一个简化的语义化版本。
type Semver struct {
	Major int
	Minor int
	Patch int
	Pre   string
}

// ParseSemver 解析 vX.Y.Z[-pre]；解析失败返回 error。
func ParseSemver(raw string) (Semver, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
	if idx := strings.Index(value, "+"); idx >= 0 {
		value = value[:idx]
	}
	pre := ""
	if idx := strings.Index(value, "-"); idx >= 0 {
		pre = value[idx+1:]
		value = value[:idx]
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Semver{}, fmt.Errorf("版本号 %q 不是 X.Y.Z 形式", raw)
	}
	nums := make([]int, 3)
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			nums[i] = 0
			continue
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return Semver{}, fmt.Errorf("版本号 %q 的数值段非法", raw)
		}
		nums[i] = n
	}
	return Semver{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, nil
}

// Compare 比较两个版本：返回 -1/0/1。
func (v Semver) Compare(other Semver) int {
	if v.Major != other.Major {
		return cmpInt(v.Major, other.Major)
	}
	if v.Minor != other.Minor {
		return cmpInt(v.Minor, other.Minor)
	}
	if v.Patch != other.Patch {
		return cmpInt(v.Patch, other.Patch)
	}
	// 有预发布标记的版本更小。
	switch {
	case v.Pre == "" && other.Pre == "":
		return 0
	case v.Pre == "":
		return 1
	case other.Pre == "":
		return -1
	default:
		return strings.Compare(v.Pre, other.Pre)
	}
}

// cmpInt 返回 a 与 b 的比较结果。
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// parseSchemaChangedMarker 从 release body 读取 `<!-- schema_changed: true -->` 标记。
func parseSchemaChangedMarker(body string) bool {
	lower := strings.ToLower(body)
	idx := strings.Index(lower, "schema_changed")
	if idx < 0 {
		return false
	}
	segment := lower[idx:]
	if end := strings.Index(segment, "-->"); end >= 0 {
		segment = segment[:end]
	}
	return strings.Contains(segment, "true")
}

// truncateNotes 截断 release notes 到 4KB（按字节安全截断 UTF-8）。
func truncateNotes(body string) string {
	if len(body) <= upgradeNotesMaxBytes {
		return body
	}
	cut := body[:upgradeNotesMaxBytes]
	for len(cut) > 0 && !isUTF8Boundary(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n…（说明过长已截断）"
}

// isUTF8Boundary 判断字节是否是 UTF-8 首字节（用于安全截断）。
func isUTF8Boundary(b byte) bool { return b&0xC0 != 0x80 }
