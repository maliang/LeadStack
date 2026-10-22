// Package credential 负责 SSH 凭证的托管与加解密。
//
// 契约见 docs/ARCHITECTURE.md 第 8.1 节（已冻结，签名不得改名）。
// 该包只依赖标准库与 golang.org/x/crypto，不得 import internal/store 或 internal/api。
package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// MasterKeySize 是主密钥字节数（AES-256）。
const MasterKeySize = 32

// Kind 是凭证类型。
type Kind string

const (
	// KindPassword 表示用户名 + 密码认证。
	KindPassword Kind = "password"
	// KindKey 表示用户名 + 私钥（可选 passphrase）认证。
	KindKey Kind = "key"
	// KindBaota 表示宝塔面板 API 凭据（面板地址 + API 密钥），不用于 SSH 登录。
	KindBaota Kind = "baota"
)

// Secret 是凭证明文结构，加密后作为 BLOB 落库。
type Secret struct {
	Kind       Kind   `json:"kind"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	PrivateKey []byte `json:"private_key,omitempty"` // PEM 原文
	Passphrase string `json:"passphrase,omitempty"`

	// 宝塔面板凭据（Kind=baota 时使用）。
	PanelURL           string `json:"panel_url,omitempty"`
	APIKey             string `json:"api_key,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
}

// LoadOrCreateMasterKey 读取 32 字节主密钥；文件不存在则安全随机构造并以 0600 落盘。
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("credential: 主密钥路径不能为空")
	}

	key, readErr := readMasterKey(path)
	if readErr == nil {
		return key, nil
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}

	// 不存在：生成随机密钥。先写临时文件并 fsync，再用 os.Link 原子发布，
	// 避免并发启动时读到半写的密钥文件（Link 已存在会失败，此时复用已有文件）。
	key = make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("credential: 生成主密钥失败: %w", err)
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("credential: 创建主密钥目录失败: %w", err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".master-key-*")
	if err != nil {
		return nil, fmt.Errorf("credential: 创建主密钥临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // Link 成功后这里是 no-op

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("credential: 设置主密钥权限失败: %w", err)
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("credential: 写入主密钥失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("credential: 落盘主密钥失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("credential: 关闭主密钥临时文件失败: %w", err)
	}

	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			// 另一个进程抢先创建，复用它的密钥。
			return reuseExistingMasterKey(path)
		}
		// 少数文件系统不支持硬链接：退化为 O_EXCL 独占创建。
		fallbackKey, fallbackErr := createMasterKeyExclusive(path, key)
		if fallbackErr == nil {
			return fallbackKey, nil
		}
		return nil, fmt.Errorf("credential: 发布主密钥文件失败: %w", errors.Join(err, fallbackErr))
	}

	// 尽力 fsync 目录，保证 rename/link 持久化。
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return key, nil
}

// reuseExistingMasterKey 读取另一个进程创建的主密钥；对“刚创建尚未写完”的文件做短暂重试。
func reuseExistingMasterKey(path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		key, err := readMasterKey(path)
		if err == nil {
			return key, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, lastErr
}

// createMasterKeyExclusive 在不支持硬链接的文件系统上退化为 O_EXCL 独占创建。
func createMasterKeyExclusive(path string, key []byte) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return reuseExistingMasterKey(path)
		}
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return key, nil
}

func readMasterKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("credential: 读取主密钥 %s 失败: %w", path, err)
	}
	if len(b) != MasterKeySize {
		return nil, fmt.Errorf("credential: 主密钥长度非法（期望 %d 字节，实际 %d 字节）", MasterKeySize, len(b))
	}
	return b, nil
}

// Cipher 使用 AES-256-GCM 加解密凭证明文。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 用 32 字节主密钥构造 Cipher。
func NewCipher(masterKey []byte) (*Cipher, error) {
	if len(masterKey) != MasterKeySize {
		return nil, fmt.Errorf("credential: 主密钥必须为 %d 字节，实际 %d 字节", MasterKeySize, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("credential: 初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential: 初始化 GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 输出 nonce || AES-256-GCM(ciphertext)，可直接存 BLOB。
func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("credential: Cipher 未初始化")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("credential: 生成 nonce 失败: %w", err)
	}
	// Seal(dst=nonce, nonce=nonce, ...) 会把密文追加在 nonce 之后，
	// 最终布局正好是 nonce || ciphertext+tag。
	return c.aead.Seal(nonce, nonce, plain, nil), nil
}

// Decrypt 解密 Encrypt 的输出。
func (c *Cipher) Decrypt(blob []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("credential: Cipher 未初始化")
	}
	nonceSize := c.aead.NonceSize()
	if len(blob) < nonceSize+c.aead.Overhead() {
		return nil, errors.New("credential: 密文长度非法")
	}
	plain, err := c.aead.Open(nil, blob[:nonceSize], blob[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("credential: 解密失败（主密钥不匹配或数据被篡改）: %w", err)
	}
	return plain, nil
}

// Marshal 把 Secret 编码为 JSON 明文。
func Marshal(s Secret) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("credential: 编码 Secret 失败: %w", err)
	}
	return b, nil
}

// Unmarshal 从 JSON 明文解码 Secret。
func Unmarshal(b []byte) (Secret, error) {
	var s Secret
	if err := json.Unmarshal(b, &s); err != nil {
		return Secret{}, fmt.Errorf("credential: 解码 Secret 失败: %w", err)
	}
	return s, nil
}

// Validate 校验凭证结构合法性。
func Validate(s Secret) error {
	if s.Username == "" {
		return errors.New("credential: username 不能为空")
	}
	switch s.Kind {
	case KindPassword:
		if s.Password == "" {
			return errors.New("credential: password 类型必须提供 password")
		}
		if len(s.PrivateKey) != 0 {
			return errors.New("credential: password 类型不能同时提供 private_key")
		}
		if s.Passphrase != "" {
			return errors.New("credential: password 类型不能提供 passphrase")
		}
	case KindBaota:
		if strings.TrimSpace(s.PanelURL) == "" {
			return errors.New("credential: baota 类型必须提供 panel_url")
		}
		if strings.TrimSpace(s.APIKey) == "" {
			return errors.New("credential: baota 类型必须提供 api_key")
		}
		if len(s.PrivateKey) != 0 || s.Password != "" || s.Passphrase != "" {
			return errors.New("credential: baota 类型不能同时提供 SSH 凭据字段")
		}
	case KindKey:
		if len(s.PrivateKey) == 0 {
			return errors.New("credential: key 类型必须提供 private_key")
		}
		if s.Password != "" {
			return errors.New("credential: key 类型不能同时提供 password")
		}
		if err := validatePrivateKey(s.PrivateKey, s.Passphrase); err != nil {
			return err
		}
	default:
		return fmt.Errorf("credential: 未知凭证类型 %q（应为 %q / %q / %q）", s.Kind, KindPassword, KindKey, KindBaota)
	}
	return nil
}

// validatePrivateKey 校验私钥能否被解析（加密私钥需要正确 passphrase）。
func validatePrivateKey(pemBytes []byte, passphrase string) error {
	var err error
	if passphrase != "" {
		_, err = ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase))
	} else {
		_, err = ssh.ParsePrivateKey(pemBytes)
	}
	if err == nil {
		return nil
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return errors.New("credential: 私钥已加密，必须提供 passphrase")
	}
	return fmt.Errorf("credential: 私钥解析失败: %w", err)
}

// Fingerprint 返回 "SHA256:<base64>"，仅对私钥有意义；password 类型返回空串。
func Fingerprint(privateKeyPEM []byte) (string, error) {
	if len(privateKeyPEM) == 0 {
		return "", nil
	}
	signer, err := ssh.ParsePrivateKey(privateKeyPEM)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return "", errors.New("credential: 私钥已加密，无法在无 passphrase 时计算指纹")
		}
		return "", fmt.Errorf("credential: 解析私钥失败: %w", err)
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}
