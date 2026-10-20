package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/maliang/leadstack/internal/config"
)

// artifactStore 负责上传制品的落盘与读取：<data_dir>/artifacts/<id>。
//
// 写入使用「临时文件 + 原子 rename」：任何一步失败都不会留下半个制品。
type artifactStore struct {
	dir      string
	maxBytes int64
}

// newArtifactStore 创建制品存储（目录不存在时按需创建）。
func newArtifactStore(dir string, maxBytes int64) (*artifactStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("初始化制品存储: 目录为空")
	}
	if maxBytes <= 0 {
		// 手工拼装的 Config（例如测试）可能没填这一项：回落到契约默认值，避免直接不可用。
		maxBytes = int64(config.DefaultArtifactMaxMB) << 20
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("创建制品目录 %s: %w", dir, err)
	}
	return &artifactStore{dir: dir, maxBytes: maxBytes}, nil
}

// MaxBytes 返回单制品体积上限。
func (s *artifactStore) MaxBytes() int64 { return s.maxBytes }

// Path 返回制品的绝对路径。
func (s *artifactStore) Path(id string) string {
	return filepath.Join(s.dir, artifactFileName(id))
}

// Save 把 r 的内容写入制品文件，返回 sha256 与写入字节数。
//
// 超过上限时报错并清理临时文件（调用方据此返回 413）。
func (s *artifactStore) Save(id string, r io.Reader) (sha256Hex string, size int64, err error) {
	if err := validateArtifactID(id); err != nil {
		return "", 0, err
	}
	// 多读 1 字节用于判断「是否超限」。
	limited := io.LimitReader(r, s.maxBytes+1)

	tmp, err := os.CreateTemp(s.dir, "."+id+".*.part")
	if err != nil {
		return "", 0, fmt.Errorf("创建制品临时文件: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		if cerr := tmp.Close(); cerr != nil {
			// 已经在错误路径上，关闭失败只忽略。
			_ = cerr
		}
		if rerr := os.Remove(tmpName); rerr != nil && !os.IsNotExist(rerr) {
			_ = rerr
		}
	}

	hasher := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, hasher), limited)
	if err != nil {
		cleanup()
		return "", 0, fmt.Errorf("写入制品内容: %w", err)
	}
	if size > s.maxBytes {
		cleanup()
		return "", 0, fmt.Errorf("%w: 制品超过 %d 字节上限", errArtifactTooLarge, s.maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("同步制品文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		if rerr := os.Remove(tmpName); rerr != nil && !os.IsNotExist(rerr) {
			_ = rerr
		}
		return "", 0, fmt.Errorf("关闭制品文件: %w", err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		if rerr := os.Remove(tmpName); rerr != nil && !os.IsNotExist(rerr) {
			_ = rerr
		}
		return "", 0, fmt.Errorf("设置制品权限: %w", err)
	}
	// 原子发布：同一 id 重复上传会覆盖（制品 id 由调用方生成，正常不会重复）。
	if err := os.Rename(tmpName, s.Path(id)); err != nil {
		if rerr := os.Remove(tmpName); rerr != nil && !os.IsNotExist(rerr) {
			_ = rerr
		}
		return "", 0, fmt.Errorf("发布制品文件: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

// Open 打开制品文件供下载。
func (s *artifactStore) Open(id string) (*os.File, error) {
	if err := validateArtifactID(id); err != nil {
		return nil, err
	}
	f, err := os.Open(s.Path(id))
	if err != nil {
		return nil, fmt.Errorf("打开制品 %s: %w", id, err)
	}
	return f, nil
}

// errArtifactTooLarge 表示制品超过体积上限（API 层映射为 413）。
var errArtifactTooLarge = errors.New("制品体积超限")

// artifactFileName 把制品 ID 映射成文件名（ID 只允许 UUID 字符集，天然安全）。
func artifactFileName(id string) string {
	return id
}

// validateArtifactID 校验制品 ID 字符集，防止路径穿越。
func validateArtifactID(id string) error {
	if id == "" || len(id) > 64 {
		return errors.New("制品 ID 非法")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("制品 ID 含非法字符 %q", r)
		}
	}
	return nil
}
