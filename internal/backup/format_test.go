package backup

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func makeTestAEAD(t *testing.T) (key, aad []byte) {
	t.Helper()
	key = randomTestBytes(t, KeyLen)
	return key, []byte(`{"format_version":1}`)
}

func TestChunkedAEADRoundTrip(t *testing.T) {
	key, aad := makeTestAEAD(t)
	aead, err := newAEAD(key)
	if err != nil {
		t.Fatalf("newAEAD: %v", err)
	}
	nonce := randomTestBytes(t, NonceLen)

	// 2.5 个分块，覆盖「整块 + 尾块」。
	plain := randomTestBytes(t, ChunkSize*2+ChunkSize/2)
	var buf bytes.Buffer
	cw := newChunkWriter(&buf, aead, nonce, aad, ChunkSize)
	if _, err := cw.Write(plain[:12345]); err != nil {
		t.Fatalf("Write #1: %v", err)
	}
	if _, err := cw.Write(plain[12345:]); err != nil {
		t.Fatalf("Write #2: %v", err)
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 密文中不应出现明文。
	if bytes.Contains(buf.Bytes(), plain[:64]) {
		t.Fatal("密文中出现明文")
	}

	cr := newChunkReader(bytes.NewReader(buf.Bytes()), aead, nonce, aad)
	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("解密读取失败: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("流式加解密结果不一致（got %d, want %d）", len(got), len(plain))
	}
}

func TestChunkedAEADDetectsTamperAndTruncation(t *testing.T) {
	key, aad := makeTestAEAD(t)
	aead, err := newAEAD(key)
	if err != nil {
		t.Fatalf("newAEAD: %v", err)
	}
	nonce := randomTestBytes(t, NonceLen)
	plain := randomTestBytes(t, ChunkSize+100)
	var buf bytes.Buffer
	cw := newChunkWriter(&buf, aead, nonce, aad, ChunkSize)
	if _, err := cw.Write(plain); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ciphertext := buf.Bytes()

	t.Run("篡改密文", func(t *testing.T) {
		tampered := append([]byte(nil), ciphertext...)
		// 第一个块：4 字节长度 + 密文，改密文中间一个字节。
		tampered[8] ^= 0x01
		_, err := io.ReadAll(newChunkReader(bytes.NewReader(tampered), aead, nonce, aad))
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("篡改密文应返回 ErrCorrupt，实际: %v", err)
		}
	})

	t.Run("篡改 AAD", func(t *testing.T) {
		wrongAAD := append([]byte(nil), aad...)
		wrongAAD[0] ^= 0x01
		_, err := io.ReadAll(newChunkReader(bytes.NewReader(ciphertext), aead, nonce, wrongAAD))
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("篡改 AAD 应返回 ErrCorrupt，实际: %v", err)
		}
	})

	t.Run("截断", func(t *testing.T) {
		truncated := ciphertext[:len(ciphertext)-16]
		_, err := io.ReadAll(newChunkReader(bytes.NewReader(truncated), aead, nonce, aad))
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("截断应返回 ErrCorrupt，实际: %v", err)
		}
	})

	t.Run("块长度非法", func(t *testing.T) {
		bad := append([]byte(nil), ciphertext...)
		bad[3] = 0xFF // 放大长度前缀
		_, err := io.ReadAll(newChunkReader(bytes.NewReader(bad), aead, nonce, aad))
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("非法块长度应返回 ErrCorrupt，实际: %v", err)
		}
	})
}

func TestParseArchiveHeaderValidation(t *testing.T) {
	salt := randomTestBytes(t, SaltLen)
	nonce := randomTestBytes(t, NonceLen)
	key := randomTestBytes(t, KeyLen)
	raw, hdr, err := newArchiveHeader("test", 6, 123, false, salt, nonce, passVerifyToken(key))
	if err != nil {
		t.Fatalf("newArchiveHeader: %v", err)
	}
	if _, err := parseArchiveHeader(raw); err != nil {
		t.Fatalf("合法 header 应通过: %v", err)
	}
	if hdr.FormatVersion != FormatVersion || hdr.ChunkSize != ChunkSize {
		t.Fatalf("header 字段异常: %+v", hdr)
	}

	// 篡改各种字段后都必须被拒绝为 ErrCorrupt。
	mutations := map[string]func(*archiveHeader){
		"格式版本":       func(h *archiveHeader) { h.FormatVersion = 2 },
		"KDF 名称":     func(h *archiveHeader) { h.KDF.Name = "pbkdf2" },
		"scrypt N":   func(h *archiveHeader) { h.KDF.N = 1 << 30 },
		"scrypt r":   func(h *archiveHeader) { h.KDF.R = 0 },
		"key_len":    func(h *archiveHeader) { h.KDF.KeyLen = 16 },
		"加密算法":       func(h *archiveHeader) { h.Cipher.Name = "aes-128-gcm" },
		"nonce":      func(h *archiveHeader) { h.Cipher.Nonce = "AAAA" },
		"chunk_size": func(h *archiveHeader) { h.ChunkSize = 4096 },
		"salt":       func(h *archiveHeader) { h.KDF.Salt = "!!!" },
		"pass_verify": func(h *archiveHeader) {
			h.PassVerify = "AAAA"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			cp := *hdr
			mutate(&cp)
			b, err := jsonMarshal(&cp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := parseArchiveHeader(b); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("非法 header（%s）应返回 ErrCorrupt，实际: %v", name, err)
			}
		})
	}
}

func TestCheckPassphrase(t *testing.T) {
	salt := randomTestBytes(t, SaltLen)
	nonce := randomTestBytes(t, NonceLen)
	key, err := deriveKey("correct-pass", salt)
	if err != nil {
		t.Fatalf("deriveKey: %v", err)
	}
	raw, hdr, err := newArchiveHeader("test", 6, 1, false, salt, nonce, passVerifyToken(key))
	if err != nil {
		t.Fatalf("newArchiveHeader: %v", err)
	}
	if _, err := checkPassphrase(hdr, "correct-pass"); err != nil {
		t.Fatalf("正确口令应通过: %v", err)
	}
	if _, err := checkPassphrase(hdr, "wrong-pass"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("错误口令应返回 ErrBadPassphrase，实际: %v", err)
	}
	_ = raw

	// 去掉口令校验值的 header 属于「格式不符」，不是口令错误。
	cp := *hdr
	cp.PassVerify = ""
	if _, err := checkPassphrase(&cp, "correct-pass"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("缺少 pass_verify 应返回 ErrCorrupt，实际: %v", err)
	}
}

func TestReadHeaderRejectsBadMagic(t *testing.T) {
	if _, _, err := readHeader(bytes.NewReader([]byte("LSBAK2\n\x00\x00\x00\x02{}"))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("错误 magic 应返回 ErrCorrupt，实际: %v", err)
	}
	if _, _, err := readHeader(bytes.NewReader([]byte("LSBAK1\n"))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("缺失 header 长度应返回 ErrCorrupt，实际: %v", err)
	}
}
