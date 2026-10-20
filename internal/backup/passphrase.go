package backup

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// passphraseEntropy 是自动生成口令时的随机字节数。
const passphraseEntropy = 32

// ResolvePassphrase 解析备份口令：explicit > 环境变量 > <dataDir>/backup.pass > 自动生成并 0600 落盘。
//
// 该函数供 CLI / 调用方复用；Manager 内部只在 Create 时允许自动生成，
// List/Verify/Restore 遇到「无口令」会报错而不是偷偷生成新口令（否则错误口令
// 会被误判成损坏）。
func ResolvePassphrase(dataDir, explicit string) (string, error) {
	return resolvePassphrase(dataDir, explicit, true)
}

func resolvePassphrase(dataDir, explicit string, allowGenerate bool) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	if env := os.Getenv(EnvPassphrase); env != "" {
		return env, nil
	}

	passFile := filepath.Join(dataDir, PassphraseFileName)
	if dataDir != "" {
		if b, err := os.ReadFile(passFile); err == nil {
			pass := strings.TrimRight(string(b), "\r\n")
			if strings.TrimSpace(pass) == "" {
				return "", fmt.Errorf("backup: 口令文件 %s 为空", passFile)
			}
			return pass, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("backup: 读取口令文件 %s 失败: %w", passFile, err)
		}
	}

	if !allowGenerate {
		return "", errors.New("backup: 未找到备份口令（请设置 " + EnvPassphrase + " 或提供 " + PassphraseFileName + "）")
	}
	if dataDir == "" {
		return "", errors.New("backup: 数据目录为空，无法生成 backup.pass")
	}

	// 自动生成：32 字节随机熵，以 base64 文本写入 0600 文件（便于 CLI --passphrase-file 使用）。
	raw := make([]byte, passphraseEntropy)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("backup: 生成备份口令失败: %w", err)
	}
	pass := base64.StdEncoding.EncodeToString(raw)
	if err := writePassphraseFile(passFile, pass); err != nil {
		return "", err
	}
	return pass, nil
}

// writePassphraseFile 以 0600 原子写入口令文件（临时文件 + fsync + rename）。
func writePassphraseFile(path, passphrase string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("backup: 创建数据目录 %s 失败: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".backup-pass-*")
	if err != nil {
		return fmt.Errorf("backup: 创建口令临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: 设置口令文件权限失败: %w", err)
	}
	if _, err := tmp.WriteString(passphrase + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: 写入口令文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: 落盘口令文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: 关闭口令临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("backup: 发布口令文件失败: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
