package credential

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newEd25519(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 ed25519 密钥失败: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("构造 ssh.PublicKey 失败: %v", err)
	}
	return priv, pub
}

func privateKeyPEM(t *testing.T, priv ed25519.PrivateKey, passphrase string) []byte {
	t.Helper()
	var (
		block *pem.Block
		err   error
	)
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "leadstack-test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "leadstack-test", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("序列化私钥失败: %v", err)
	}
	return pem.EncodeToMemory(block)
}

func TestCipherRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, MasterKeySize)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	plain := []byte(`{"kind":"password","username":"root","password":"s3cr3t"}`)
	blob, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(blob, plain) {
		t.Fatal("密文中不应出现明文")
	}
	if len(blob) != 12+len(plain)+16 { // nonce || ciphertext+tag
		t.Fatalf("密文长度不符合 nonce||GCM 布局: %d", len(blob))
	}

	got, err := c.Decrypt(blob)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("解密结果不一致: %q", got)
	}

	// 同一明文两次加密的 nonce 必须不同。
	blob2, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt #2: %v", err)
	}
	if bytes.Equal(blob[:12], blob2[:12]) {
		t.Fatal("两次加密的 nonce 相同")
	}
}

func TestCipherTamperDetection(t *testing.T) {
	c, err := NewCipher(bytes.Repeat([]byte{0x01}, MasterKeySize))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	blob, err := c.Encrypt([]byte("top-secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	t.Run("翻转密文字节", func(t *testing.T) {
		tampered := append([]byte(nil), blob...)
		tampered[len(tampered)-1] ^= 0xFF
		if _, err := c.Decrypt(tampered); err == nil {
			t.Fatal("篡改后的密文必须解密失败")
		}
	})
	t.Run("翻转 nonce 字节", func(t *testing.T) {
		tampered := append([]byte(nil), blob...)
		tampered[0] ^= 0xFF
		if _, err := c.Decrypt(tampered); err == nil {
			t.Fatal("篡改 nonce 后必须解密失败")
		}
	})
	t.Run("截断", func(t *testing.T) {
		if _, err := c.Decrypt(blob[:len(blob)-1]); err == nil {
			t.Fatal("截断后的密文必须解密失败")
		}
		if _, err := c.Decrypt(nil); err == nil {
			t.Fatal("空密文必须解密失败")
		}
	})
	t.Run("错误主密钥", func(t *testing.T) {
		other, err := NewCipher(bytes.Repeat([]byte{0x02}, MasterKeySize))
		if err != nil {
			t.Fatalf("NewCipher: %v", err)
		}
		if _, err := other.Decrypt(blob); err == nil {
			t.Fatal("错误主密钥必须解密失败")
		}
	})
}

func TestNewCipherRejectsBadKeySize(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, 31, 33, 64} {
		if _, err := NewCipher(make([]byte, n)); err == nil {
			t.Fatalf("长度为 %d 的主密钥应被拒绝", n)
		}
	}
	if _, err := NewCipher(make([]byte, 32)); err != nil {
		t.Fatalf("32 字节主密钥应被接受: %v", err)
	}
}

func TestLoadOrCreateMasterKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "master.key")

	key1, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("首次创建主密钥失败: %v", err)
	}
	if len(key1) != MasterKeySize {
		t.Fatalf("主密钥长度 = %d, 期望 %d", len(key1), MasterKeySize)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("主密钥文件不存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("主密钥文件权限 = %o, 期望 600", perm)
	}

	// 再次读取必须拿到同一把密钥。
	key2, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("再次读取主密钥失败: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("两次读取的主密钥不一致")
	}

	// 长度非法的已有文件必须报错，而不是悄悄覆盖。
	badPath := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(badPath, []byte("short"), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	if _, err := LoadOrCreateMasterKey(badPath); err == nil {
		t.Fatal("非法长度主密钥文件应报错")
	}
}

func TestLoadOrCreateMasterKeyConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	const n = 8

	results := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = LoadOrCreateMasterKey(path)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d 创建主密钥失败: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("并发创建得到不同主密钥（0 vs %d）", i)
		}
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	priv, _ := newEd25519(t)
	in := Secret{
		Kind:       KindKey,
		Username:   "deploy",
		PrivateKey: privateKeyPEM(t, priv, ""),
		Passphrase: "pp",
	}
	b, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Kind != in.Kind || out.Username != in.Username || out.Passphrase != in.Passphrase {
		t.Fatalf("往返后字段不一致: %+v", out)
	}
	if !bytes.Equal(out.PrivateKey, in.PrivateKey) {
		t.Fatal("往返后私钥不一致")
	}
	if _, err := Unmarshal([]byte("not-json")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestValidate(t *testing.T) {
	priv, _ := newEd25519(t)
	plainKey := privateKeyPEM(t, priv, "")
	encKey := privateKeyPEM(t, priv, "correct-passphrase")

	tests := []struct {
		name    string
		secret  Secret
		wantErr bool
	}{
		{
			name:   "密码凭证合法",
			secret: Secret{Kind: KindPassword, Username: "root", Password: "pw"},
		},
		{
			name:   "未加密私钥合法",
			secret: Secret{Kind: KindKey, Username: "root", PrivateKey: plainKey},
		},
		{
			name:   "带正确 passphrase 的私钥合法",
			secret: Secret{Kind: KindKey, Username: "root", PrivateKey: encKey, Passphrase: "correct-passphrase"},
		},
		{
			name:    "缺少 username",
			secret:  Secret{Kind: KindPassword, Password: "pw"},
			wantErr: true,
		},
		{
			name:    "password 类型无密码",
			secret:  Secret{Kind: KindPassword, Username: "root"},
			wantErr: true,
		},
		{
			name:    "password 类型带私钥",
			secret:  Secret{Kind: KindPassword, Username: "root", Password: "pw", PrivateKey: plainKey},
			wantErr: true,
		},
		{
			name:    "key 类型无私钥",
			secret:  Secret{Kind: KindKey, Username: "root"},
			wantErr: true,
		},
		{
			name:    "key 类型带密码",
			secret:  Secret{Kind: KindKey, Username: "root", PrivateKey: plainKey, Password: "pw"},
			wantErr: true,
		},
		{
			name:    "加密私钥缺少 passphrase",
			secret:  Secret{Kind: KindKey, Username: "root", PrivateKey: encKey},
			wantErr: true,
		},
		{
			name:    "加密私钥 passphrase 错误",
			secret:  Secret{Kind: KindKey, Username: "root", PrivateKey: encKey, Passphrase: "wrong"},
			wantErr: true,
		},
		{
			name:    "私钥内容非法",
			secret:  Secret{Kind: KindKey, Username: "root", PrivateKey: []byte("not a pem")},
			wantErr: true,
		},
		{
			name:    "未知 kind",
			secret:  Secret{Kind: Kind("token"), Username: "root", Password: "pw"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.secret)
			if tc.wantErr && err == nil {
				t.Fatal("期望校验失败，实际通过")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望校验通过，实际失败: %v", err)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	priv, pub := newEd25519(t)
	pemBytes := privateKeyPEM(t, priv, "")

	got, err := Fingerprint(pemBytes)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	want := ssh.FingerprintSHA256(pub)
	if got != want {
		t.Fatalf("指纹 = %q, 期望 %q", got, want)
	}
	if !strings.HasPrefix(got, "SHA256:") {
		t.Fatalf("指纹格式错误: %q", got)
	}

	// password 类型（无密钥）返回空串而不是错误。
	if fp, err := Fingerprint(nil); err != nil || fp != "" {
		t.Fatalf("空私钥应返回空串: fp=%q err=%v", fp, err)
	}

	if _, err := Fingerprint([]byte("not a key")); err == nil {
		t.Fatal("非法私钥应报错")
	}

	enc := privateKeyPEM(t, priv, "pp")
	if _, err := Fingerprint(enc); err == nil {
		t.Fatal("无 passphrase 时加密私钥应报错")
	}
}
