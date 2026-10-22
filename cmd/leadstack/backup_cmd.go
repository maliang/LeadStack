package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maliang/leadstack/internal/backup"
	"github.com/maliang/leadstack/internal/config"
)

// currentSchemaVersion 是当前程序支持的最高迁移版本，用于拒绝「来自更新版本」的备份。
//
// 维护约定：internal/store/migrations 新增迁移时同步 +1（当前最高为 0007）。
// 说明：internal/store 未导出 schema 版本常量（本轮写文件范围不含 store），
// 若后续 store 导出该值，这里应改为调用它。
const currentSchemaVersion = 9

// pidFileName 是服务运行期的 pid 文件名（供 CLI 判断数据目录是否被占用）。
const pidFileName = "leadstack.pid"

// runBackupCommand 处理 `leadstack backup ...` 子命令。
//
// 用法：
//
//	leadstack backup --out /tmp/x.lsbak [--with-artifacts] [--data-dir ./data] [--passphrase-file p]
//	leadstack backup verify --file /tmp/x.lsbak [--data-dir ./data] [--passphrase-file p]
func runBackupCommand(args []string) error {
	if len(args) > 0 && args[0] == "verify" {
		return runBackupVerify(args[1:])
	}
	return runBackupCreate(args)
}

// runBackupCreate 生成一份备份到指定路径（CLI 用 CreateTo，不经过默认备份目录）。
func runBackupCreate(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", envOr(config.EnvDataDir, "./data"), "数据目录")
	out := fs.String("out", "", "输出文件路径（必填）")
	withArtifacts := fs.Bool("with-artifacts", false, "是否包含上传制品")
	passFile := fs.String("passphrase-file", "", "备份口令文件（可选）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数: %w", err)
	}
	if strings.TrimSpace(*out) == "" {
		return fmt.Errorf("缺少 --out 参数\n用法: leadstack backup --out <file> [--with-artifacts] [--data-dir <dir>] [--passphrase-file <file>]")
	}
	absDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		return fmt.Errorf("解析数据目录 %q: %w", *dataDir, err)
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		return fmt.Errorf("解析输出路径 %q: %w", *out, err)
	}
	// 创建时允许口令缺失：Manager 会按 env → backup.pass → 自动生成 0600 的顺序解析。
	pass, err := resolvePassphrase(absDataDir, *passFile, false)
	if err != nil {
		return err
	}
	manager, err := backup.NewManager(backup.Options{
		DataDir: absDataDir, BackupDir: filepath.Join(absDataDir, config.BackupsDirName),
		Passphrase: pass, AppVersion: version,
	})
	if err != nil {
		return fmt.Errorf("初始化备份管理器: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	info, err := manager.CreateTo(ctx, absOut, *withArtifacts)
	if err != nil {
		return fmt.Errorf("创建备份失败: %w", err)
	}
	fmt.Printf("备份完成\n  文件: %s\n  大小: %d 字节\n  sha256: %s\n  含制品: %t\n  数据库版本: %d\n",
		absOut, info.SizeBytes, info.SHA256, info.IncludesArtifacts, info.SchemaVersion)
	return nil
}

// runBackupVerify 校验一个归档文件（口令错/损坏/版本过新分别给出可执行提示）。
func runBackupVerify(args []string) error {
	fs := flag.NewFlagSet("backup verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", envOr(config.EnvDataDir, "./data"), "数据目录（用于定位 backup.pass）")
	file := fs.String("file", "", "要校验的归档文件（必填）")
	passFile := fs.String("passphrase-file", "", "备份口令文件（可选）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数: %w", err)
	}
	if strings.TrimSpace(*file) == "" {
		return fmt.Errorf("缺少 --file 参数\n用法: leadstack backup verify --file <file> [--data-dir <dir>] [--passphrase-file <file>]")
	}
	absDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		return fmt.Errorf("解析数据目录 %q: %w", *dataDir, err)
	}
	absFile, err := filepath.Abs(*file)
	if err != nil {
		return fmt.Errorf("解析归档路径 %q: %w", *file, err)
	}
	// 校验必须使用既有口令：绝不自动生成（否则会得到误导性的「口令错误」）。
	pass, err := resolvePassphrase(absDataDir, *passFile, true)
	if err != nil {
		return err
	}
	manager, err := backup.NewManager(backup.Options{
		DataDir: absDataDir, BackupDir: filepath.Join(absDataDir, config.BackupsDirName),
		Passphrase: pass, AppVersion: version,
	})
	if err != nil {
		return fmt.Errorf("初始化备份管理器: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	manifest, err := manager.Verify(ctx, absFile)
	if err != nil {
		return fmt.Errorf("校验失败: %w", backupErrorHint(err))
	}
	fmt.Printf("校验通过\n  文件: %s\n  应用版本: %s\n  数据库版本: %d\n  含制品: %t\n  创建时间: %s\n  文件清单:\n",
		absFile, manifest.AppVersion, manifest.SchemaVersion, manifest.IncludesArtifacts,
		time.UnixMilli(manifest.CreatedAt).Format(time.RFC3339))
	for _, f := range manifest.Files {
		fmt.Printf("    %-24s %10d 字节  sha256=%s\n", f.Name, f.SizeBytes, f.SHA256)
	}
	return nil
}

// runRestoreCommand 处理 `leadstack restore ...`：要求服务已停止。
func runRestoreCommand(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", envOr(config.EnvDataDir, "./data"), "数据目录")
	file := fs.String("file", "", "要恢复的归档文件（必填）")
	passFile := fs.String("passphrase-file", "", "备份口令文件（可选）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数: %w", err)
	}
	if strings.TrimSpace(*file) == "" {
		return fmt.Errorf("缺少 --file 参数\n用法: leadstack restore --file <file> --data-dir <dir> [--passphrase-file <file>]")
	}
	absDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		return fmt.Errorf("解析数据目录 %q: %w", *dataDir, err)
	}
	absFile, err := filepath.Abs(*file)
	if err != nil {
		return fmt.Errorf("解析归档路径 %q: %w", *file, err)
	}

	// 服务必须已停止：停服务后文件句柄/pid 文件可能还有几百毫秒延迟，这里给 5s 容错。
	if err := waitDataDirIdle(absDataDir, 5*time.Second); err != nil {
		return err
	}
	pass, err := resolvePassphrase(absDataDir, *passFile, true)
	if err != nil {
		return err
	}
	res, err := backup.Restore(backup.RestoreOptions{
		File: absFile, DataDir: absDataDir, Passphrase: pass,
		MaxSchemaVersion: currentSchemaVersion,
	})
	if err != nil {
		return fmt.Errorf("恢复失败: %w\n  数据目录未被替换；原数据仍在 %s（如已有 pre-restore 目录请人工确认后再决定是否清理）",
			err, absDataDir)
	}
	fmt.Printf("恢复完成\n  恢复文件: %s\n  恢复前数据保留在: %s\n  数据库版本: %d\n",
		strings.Join(res.RestoredFiles, ", "), res.PreRestoreDir, res.Manifest.SchemaVersion)
	return nil
}

// resolvePassphrase 解析备份口令：--passphrase-file > 环境变量 > <data-dir>/backup.pass。
//
// required=false 时允许返回空串（由 backup.NewManager 自动生成口令）；
// required=true（校验/恢复）时找不到口令直接报错，**绝不自动生成**。
func resolvePassphrase(dataDir, passFile string, required bool) (string, error) {
	if strings.TrimSpace(passFile) != "" {
		raw, err := os.ReadFile(passFile)
		if err != nil {
			return "", fmt.Errorf("读取口令文件 %s: %w", passFile, err)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return "", fmt.Errorf("口令文件 %s 为空", passFile)
		}
		return value, nil
	}
	if env := strings.TrimSpace(os.Getenv(config.EnvBackupPassphrase)); env != "" {
		return env, nil
	}
	if raw, err := os.ReadFile(filepath.Join(dataDir, "backup.pass")); err == nil {
		if value := strings.TrimSpace(string(raw)); value != "" {
			return value, nil
		}
	}
	if !required {
		return "", nil
	}
	return "", fmt.Errorf("未找到备份口令：请设置环境变量 %s、通过 --passphrase-file 指定口令文件，或确认 %s 存在",
		config.EnvBackupPassphrase, filepath.Join(dataDir, "backup.pass"))
}

// backupErrorHint 为常见失败补充可执行提示。
func backupErrorHint(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backup.ErrBadPassphrase):
		return fmt.Errorf("%w\n  提示: 请检查 backup.pass 或 LEADSTACK_BACKUP_PASSPHRASE 是否与备份时一致", err)
	case errors.Is(err, backup.ErrCorrupt):
		return fmt.Errorf("%w\n  提示: 文件可能不完整或被篡改，建议重新获取该备份文件", err)
	case errors.Is(err, backup.ErrSchemaTooNew):
		return fmt.Errorf("%w\n  提示: 该备份由更新版本的 LeadStack 创建，请先升级程序", err)
	default:
		return err
	}
}

// waitDataDirIdle 等待数据目录不再被运行中的服务占用。
//
// 判定依据：<data-dir>/leadstack.pid 存在且其中的进程仍存活。
// 停服务后 pid 文件可能还有延迟（进程正在退出），因此按 200ms 轮询重试到 timeout。
func waitDataDirIdle(dataDir string, timeout time.Duration) error {
	pidPath := filepath.Join(dataDir, pidFileName)
	deadline := time.Now().Add(timeout)
	for {
		pid, err := readPIDFile(pidPath)
		if err != nil {
			return err
		}
		if pid <= 0 {
			return nil // 没有 pid 文件：视为已停止
		}
		if !processAlive(pid) {
			return nil // 陈旧 pid 文件：进程已不存在
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("数据目录仍被占用（进程 pid=%d 仍在运行）：请先停止 LeadStack 服务再执行恢复", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// readPIDFile 读取 pid 文件；不存在返回 (0, nil)。
func readPIDFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取 pid 文件 %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, nil // 内容非法按陈旧文件处理
	}
	return pid, nil
}

// writePIDFile 写入当前进程的 pid（服务启动时调用）。
func writePIDFile(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, pidFileName)
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("写入 pid 文件 %s: %w", path, err)
	}
	return nil
}

// removePIDFile 删除 pid 文件（优雅关闭时调用）。
func removePIDFile(dataDir string) {
	path := filepath.Join(dataDir, pidFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "清理 pid 文件失败: %v\n", err)
	}
}

// envOr 返回环境变量值或默认值。
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
