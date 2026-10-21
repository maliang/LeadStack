package auth

import (
	"fmt"
	"sort"
	"strings"
)

// 权限点：能力即权限。角色 = 权限集合 + 资源范围（契约 §52/§53）。
//
// 新增权限点必须同时更新：本文件的目录、内置角色集合、docs/ARCHITECTURE.md §52.1，
// 以及前端 i18n 文案（权限展示名以本目录为准，前端只做兜底）。
const (
	PermServerRead    = "server.read"
	PermServerWrite   = "server.write"
	PermServerDelete  = "server.delete"
	PermTerminalUse   = "terminal.use"
	PermCheckRead     = "check.read"
	PermCheckWrite    = "check.write"
	PermAlertRead     = "alert.read"
	PermAlertWrite    = "alert.write"
	PermDeployRead    = "deploy.read"
	PermDeployWrite   = "deploy.write"
	PermBackupRead    = "backup.read"
	PermBackupWrite   = "backup.write"
	PermBackupDelete  = "backup.delete"
	PermBackupRestore = "backup.restore"
	PermTaskRead      = "task.read"
	PermTaskWrite     = "task.write"
	PermGroupWrite    = "group.write"
	PermAgentUpgrade  = "agent.upgrade"
	PermUserManage    = "user.manage"
	PermRoleManage    = "role.manage"
	PermTokenManage   = "token.manage"
	PermEnrollManage  = "enroll.manage"
	PermSystemManage  = "system.manage"
	PermAuditRead     = "audit.read"
)

// 角色范围模式（契约 §53.2）。
const (
	ScopeAll     = "all"
	ScopeGroups  = "groups"
	ScopeServers = "servers"
)

// Permission 是权限点的对外描述（供 /permissions 与前端勾选面板）。
type Permission struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	AdminOnly   bool   `json:"admin_only"`
}

// permissionCatalog 是权限点的唯一事实来源，顺序即 UI 展示顺序。
var permissionCatalog = []Permission{
	{PermServerRead, "查看服务器", "服务器、分组、指标、主机信息", false},
	{PermServerWrite, "纳管与修改服务器", "新增/导入/改配置/测试连通/刷新主机信息/调整分组归属", false},
	{PermServerDelete, "删除服务器", "移除纳管（不影响目标机上的 agent）", false},
	{PermTerminalUse, "终端与文件", "终端、文件上传下载、日志、进程、端口", false},
	{PermCheckRead, "查看检测", "检测项、目标、结果时间线与摘要", false},
	{PermCheckWrite, "管理检测", "增删改检测项、设目标、立即运行", false},
	{PermAlertRead, "查看告警", "告警列表、详情、摘要", false},
	{PermAlertWrite, "管理告警", "规则、渠道、静默、确认", false},
	{PermDeployRead, "查看发布", "应用、模板、制品、发布与运行日志", false},
	{PermDeployWrite, "执行发布", "应用/模板/制品/发布，含重试、回滚、取消", false},
	{PermBackupRead, "查看备份", "备份列表与备份设置", false},
	{PermBackupWrite, "创建备份", "创建备份、修改备份设置", false},
	{PermBackupDelete, "删除备份", "删除备份归档", false},
	{PermBackupRestore, "恢复备份", "校验与恢复（含上传恢复）", false},
	{PermTaskRead, "查看定时任务", "定时任务与执行历史", false},
	{PermTaskWrite, "管理定时任务", "增删改定时任务、立即执行", false},
	{PermGroupWrite, "管理分组", "分组增删改与成员调整（要求全局范围）", false},
	{PermAgentUpgrade, "升级 agent", "单机/批量升级 agent", false},
	{PermUserManage, "管理用户", "用户增删改、解锁", true},
	{PermRoleManage, "管理角色", "角色与资源范围管理", true},
	{PermTokenManage, "管理 API Token", "签发与撤销 API Token", true},
	{PermEnrollManage, "管理注册令牌", "签发与吊销 agent 注册令牌", true},
	{PermSystemManage, "系统设置", "监听地址、对外地址、控制端自升级", true},
	{PermAuditRead, "查看审计日志", "审计日志检索与导出", true},
}

// Permissions 返回权限目录的副本（避免调用方改动全局表）。
func Permissions() []Permission {
	out := make([]Permission, len(permissionCatalog))
	copy(out, permissionCatalog)
	return out
}

// permissionKeys 返回全部合法权限点。
func permissionKeys() []string {
	out := make([]string, 0, len(permissionCatalog))
	for _, p := range permissionCatalog {
		out = append(out, p.Key)
	}
	return out
}

// IsAdminOnly 判断权限点是否只能由内置 admin 持有（防提权）。
func IsAdminOnly(key string) bool {
	for _, p := range permissionCatalog {
		if p.Key == key {
			return p.AdminOnly
		}
	}
	return false
}

// PermissionExists 判断权限点是否存在。
func PermissionExists(key string) bool {
	for _, p := range permissionCatalog {
		if p.Key == key {
			return true
		}
	}
	return false
}

// UnknownPermissions 返回不在目录里的权限点（保持输入顺序）。
func UnknownPermissions(keys []string) []string {
	var out []string
	for _, k := range keys {
		if !PermissionExists(k) {
			out = append(out, k)
		}
	}
	return out
}

// AdminOnlyPermissions 返回全部「仅内置管理员」的权限点。
func AdminOnlyPermissions() []string {
	out := make([]string, 0, 6)
	for _, p := range permissionCatalog {
		if p.AdminOnly {
			out = append(out, p.Key)
		}
	}
	return out
}

// NormalizePermissions 校验并规整自定义角色的权限集合：
// 去除空白与重复、按目录顺序排序，未知权限点或管理权限一律报错（防提权）。
func NormalizePermissions(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		if !PermissionExists(key) {
			return nil, fmt.Errorf("未知权限点: %s", key)
		}
		if IsAdminOnly(key) {
			return nil, fmt.Errorf("权限点 %s 仅内置管理员可持有，不能授予自定义角色", key)
		}
		seen[key] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("权限集合不能为空")
	}
	out := make([]string, 0, len(seen))
	for _, key := range permissionKeys() {
		if _, ok := seen[key]; ok {
			out = append(out, key)
		}
	}
	return out, nil
}

// Role 是角色的统一视图：内置角色来自代码常量，自定义角色来自 roles 表。
type Role struct {
	Key         string
	Name        string
	Description string
	Permissions []string
	Builtin     bool
	ScopeMode   string
	GroupIDs    []string
	ServerIDs   []string
}

// HasPermission 判断权限集合里是否包含指定权限。
func HasPermission(set []string, want string) bool {
	for _, k := range set {
		if k == want {
			return true
		}
	}
	return false
}

// ValidScopeMode 判断范围模式是否合法。
func ValidScopeMode(mode string) bool {
	switch mode {
	case ScopeAll, ScopeGroups, ScopeServers:
		return true
	default:
		return false
	}
}

// ViewerPermissions 是内置只读角色的权限集合。
func ViewerPermissions() []string {
	return []string{
		PermServerRead, PermCheckRead, PermAlertRead,
		PermDeployRead, PermBackupRead, PermTaskRead,
	}
}

// OperatorPermissions 是内置运维角色的权限集合。
func OperatorPermissions() []string {
	return append(ViewerPermissions(),
		PermServerWrite, PermTerminalUse, PermCheckWrite, PermAlertWrite,
		PermDeployWrite, PermBackupWrite, PermTaskWrite, PermGroupWrite)
}

// AdminPermissions 是内置管理员角色的权限集合（全部权限点，按目录顺序）。
func AdminPermissions() []string {
	return permissionKeys()
}

// BuiltinRoles 返回内置角色（顺序：admin、operator、viewer）。
func BuiltinRoles() []Role {
	return []Role{
		{Key: "admin", Name: "管理员", Description: "全部权限，含用户/角色/系统设置/审计", Permissions: AdminPermissions(), Builtin: true, ScopeMode: ScopeAll},
		{Key: "operator", Name: "运维", Description: "日常运维：纳管、终端、检测、告警、发布、备份、定时任务", Permissions: OperatorPermissions(), Builtin: true, ScopeMode: ScopeAll},
		{Key: "viewer", Name: "只读", Description: "只能查看，不能执行任何写操作", Permissions: ViewerPermissions(), Builtin: true, ScopeMode: ScopeAll},
	}
}

// BuiltinRole 按 key 查内置角色。
func BuiltinRole(key string) (Role, bool) {
	for _, r := range BuiltinRoles() {
		if r.Key == key {
			return r, true
		}
	}
	return Role{}, false
}

// RolePermissions 返回内置角色的权限集合；不存在返回 false。
func RolePermissions(key string) ([]string, bool) {
	if r, ok := BuiltinRole(key); ok {
		return r.Permissions, true
	}
	return nil, false
}

// IsBuiltinRole 判断是否内置角色 key。
func IsBuiltinRole(key string) bool {
	_, ok := BuiltinRole(key)
	return ok
}

// SortedRoleKeys 返回内置角色 key（admin 在前），便于日志与测试。
func SortedRoleKeys() []string {
	out := []string{"admin", "operator", "viewer"}
	sort.Strings(out)
	return out
}
