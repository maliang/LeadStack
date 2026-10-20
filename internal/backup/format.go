package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

// maxHeaderLen 防止损坏/恶意的 headerLen 导致超大内存分配。
const maxHeaderLen = 1 << 20

// kdfParams 是 header 中的 KDF 参数（JSON 字段与契约一致）。
type kdfParams struct {
	Name   string `json:"name"`
	Salt   string `json:"salt"`
	N      int    `json:"n"`
	R      int    `json:"r"`
	P      int    `json:"p"`
	KeyLen int    `json:"key_len"`
}

// cipherParams 是 header 中的加密参数。
type cipherParams struct {
	Name  string `json:"name"`
	Nonce string `json:"nonce"`
}

// archiveHeader 是 LSBAK1 的明文 header。
//
// 契约（第 30 节）冻结了 format_version/app_version/schema_version/created_at/
// includes_artifacts/kdf/cipher 这些字段；本实现额外增加：
//   - pass_verify：HMAC-SHA256(key, "lsbak-verify") 的前 8 字节（base64），
//     用于在解密前区分「口令错误」与「归档损坏」；
//   - chunk_size：载荷 AES-GCM 分块大小（格式版本 1 固定 1 MiB）。
type archiveHeader struct {
	FormatVersion     int          `json:"format_version"`
	AppVersion        string       `json:"app_version"`
	SchemaVersion     int          `json:"schema_version"`
	CreatedAt         int64        `json:"created_at"`
	IncludesArtifacts bool         `json:"includes_artifacts"`
	KDF               kdfParams    `json:"kdf"`
	Cipher            cipherParams `json:"cipher"`
	ChunkSize         int          `json:"chunk_size,omitempty"`
	PassVerify        string       `json:"pass_verify"`
}

// newArchiveHeader 生成并编码一个 header；调用方负责随机 salt/nonce 与口令校验值。
func newArchiveHeader(appVersion string, schemaVersion int, createdAt int64, includesArtifacts bool,
	salt, nonce, passVerify []byte) ([]byte, *archiveHeader, error) {
	h := &archiveHeader{
		FormatVersion:     FormatVersion,
		AppVersion:        appVersion,
		SchemaVersion:     schemaVersion,
		CreatedAt:         createdAt,
		IncludesArtifacts: includesArtifacts,
		KDF: kdfParams{
			Name:   KDFName,
			Salt:   base64.StdEncoding.EncodeToString(salt),
			N:      KDFN,
			R:      KDFR,
			P:      KDFP,
			KeyLen: KeyLen,
		},
		Cipher: cipherParams{
			Name:  CipherName,
			Nonce: base64.StdEncoding.EncodeToString(nonce),
		},
		ChunkSize:  ChunkSize,
		PassVerify: base64.StdEncoding.EncodeToString(passVerify),
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: 编码归档 header 失败: %w", err)
	}
	return b, h, nil
}

// parseArchiveHeader 解析 header 字节并做严格校验。
func parseArchiveHeader(raw []byte) (*archiveHeader, error) {
	var h archiveHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("%w: 解析 header 失败: %v", ErrCorrupt, err)
	}
	if h.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("%w: 不支持的归档格式版本 %d", ErrCorrupt, h.FormatVersion)
	}
	if h.KDF.Name != KDFName || h.Cipher.Name != CipherName {
		return nil, fmt.Errorf("%w: 不支持的 KDF/加密算法 %q/%q", ErrCorrupt, h.KDF.Name, h.Cipher.Name)
	}
	// 对 KDF 参数设上限，避免恶意 header 触发内存/CPU DoS（正常归档就是 32768/8/1）。
	switch {
	case h.KDF.N < 1<<10 || h.KDF.N > 1<<20 || h.KDF.N&(h.KDF.N-1) != 0:
		return nil, fmt.Errorf("%w: 非法 scrypt N 参数 %d", ErrCorrupt, h.KDF.N)
	case h.KDF.R < 1 || h.KDF.R > 32:
		return nil, fmt.Errorf("%w: 非法 scrypt r 参数 %d", ErrCorrupt, h.KDF.R)
	case h.KDF.P < 1 || h.KDF.P > 16:
		return nil, fmt.Errorf("%w: 非法 scrypt p 参数 %d", ErrCorrupt, h.KDF.P)
	case h.KDF.KeyLen != KeyLen:
		return nil, fmt.Errorf("%w: 非法 key_len %d", ErrCorrupt, h.KDF.KeyLen)
	}
	if h.ChunkSize == 0 {
		h.ChunkSize = ChunkSize
	}
	if h.ChunkSize != ChunkSize {
		return nil, fmt.Errorf("%w: 不支持的 chunk_size %d", ErrCorrupt, h.ChunkSize)
	}
	if _, err := h.saltBytes(); err != nil {
		return nil, err
	}
	if nonce, err := h.nonceBytes(); err != nil {
		return nil, err
	} else if len(nonce) != NonceLen {
		return nil, fmt.Errorf("%w: nonce 长度非法", ErrCorrupt)
	}
	if verify, err := base64.StdEncoding.DecodeString(h.PassVerify); err != nil || len(verify) != VerifyTokenLen {
		return nil, fmt.Errorf("%w: pass_verify 编码或长度非法", ErrCorrupt)
	}
	return &h, nil
}

func (h *archiveHeader) saltBytes() ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(h.KDF.Salt)
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return nil, fmt.Errorf("%w: salt 非法", ErrCorrupt)
	}
	return salt, nil
}

func (h *archiveHeader) nonceBytes() ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(h.Cipher.Nonce)
	if err != nil || len(nonce) != NonceLen {
		return nil, fmt.Errorf("%w: nonce 非法", ErrCorrupt)
	}
	return nonce, nil
}

func (h *archiveHeader) passVerifyBytes() ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(h.PassVerify)
	if err != nil || len(b) != VerifyTokenLen {
		return nil, fmt.Errorf("%w: pass_verify 非法", ErrCorrupt)
	}
	return b, nil
}

// deriveKey 用 scrypt 派生 32 字节 AES-256 密钥。
func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	key, err := scrypt.Key([]byte(passphrase), salt, KDFN, KDFR, KDFP, KeyLen)
	if err != nil {
		return nil, fmt.Errorf("backup: 派生密钥失败: %w", err)
	}
	return key, nil
}

// passVerifyToken 计算 header 中的口令校验值。
func passVerifyToken(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(VerifyTokenLabel))
	return mac.Sum(nil)[:VerifyTokenLen]
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("backup: 密钥长度非法 %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backup: 初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backup: 初始化 GCM 失败: %w", err)
	}
	return aead, nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("backup: 生成随机数失败: %w", err)
	}
	return b, nil
}

// writeHeader 写出 magic + headerLen + header JSON。
func writeHeader(w io.Writer, headerBytes []byte) error {
	if len(headerBytes) == 0 || len(headerBytes) > maxHeaderLen {
		return errors.New("backup: header 长度非法")
	}
	if _, err := w.Write([]byte(Magic)); err != nil {
		return fmt.Errorf("backup: 写归档 magic 失败: %w", err)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(headerBytes)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("backup: 写 header 长度失败: %w", err)
	}
	if _, err := w.Write(headerBytes); err != nil {
		return fmt.Errorf("backup: 写 header 失败: %w", err)
	}
	return nil
}

// readHeader 读取并解析 header；返回原始 JSON 字节（作为载荷 GCM 的 AAD 绑定 header）。
func readHeader(r io.Reader) (headerBytes []byte, h *archiveHeader, err error) {
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, nil, fmt.Errorf("%w: 读取归档 magic 失败: %v", ErrCorrupt, err)
	}
	if string(magic) != Magic {
		return nil, nil, fmt.Errorf("%w: 归档 magic 不符（不是 LSBAK1）", ErrCorrupt)
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, nil, fmt.Errorf("%w: 读取 header 长度失败: %v", ErrCorrupt, err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > maxHeaderLen {
		return nil, nil, fmt.Errorf("%w: header 长度非法 %d", ErrCorrupt, n)
	}
	headerBytes = make([]byte, n)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return nil, nil, fmt.Errorf("%w: 读取 header 失败: %v", ErrCorrupt, err)
	}
	h, err = parseArchiveHeader(headerBytes)
	if err != nil {
		return nil, nil, err
	}
	return headerBytes, h, nil
}

// checkPassphrase 派生密钥并校验 header 中的口令校验值。
// 校验值不匹配返回 ErrBadPassphrase，从而避免「口令错」被误报成「归档损坏」。
func checkPassphrase(h *archiveHeader, passphrase string) ([]byte, error) {
	salt, err := h.saltBytes()
	if err != nil {
		return nil, err
	}
	want, err := h.passVerifyBytes()
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(passVerifyToken(key), want) != 1 {
		return nil, ErrBadPassphrase
	}
	return key, nil
}

// chunkNonce 由 header 中的基础 nonce 派生第 counter 块的 nonce：
// 前 8 字节固定、后 4 字节为大端 counter。格式版本 1 下与解密端约定一致。
func chunkNonce(base []byte, counter uint32) []byte {
	nonce := make([]byte, len(base))
	copy(nonce, base)
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], counter)
	return nonce
}

// chunkWriter 把明文按 ChunkSize 切块，逐块 AES-256-GCM 加密后写入底层流。
//
// 这样可以在不把整个 tar.gz 读入内存的前提下提供分段完整性认证；
// 每一块都以 header 原始 JSON 作为 AAD，因此 header 被篡改会导致载荷认证失败。
type chunkWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	nonce   []byte
	aad     []byte
	buf     []byte
	counter uint32
}

func newChunkWriter(w io.Writer, aead cipher.AEAD, nonce, aad []byte, chunkSize int) *chunkWriter {
	return &chunkWriter{w: w, aead: aead, nonce: nonce, aad: aad, buf: make([]byte, 0, chunkSize)}
}

func (cw *chunkWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		space := ChunkSize - len(cw.buf)
		if space > len(p) {
			space = len(p)
		}
		cw.buf = append(cw.buf, p[:space]...)
		p = p[space:]
		if len(cw.buf) == ChunkSize {
			if err := cw.flushChunk(); err != nil {
				return total - len(p), err
			}
		}
	}
	return total, nil
}

func (cw *chunkWriter) flushChunk() error {
	if len(cw.buf) == 0 {
		return nil
	}
	if cw.counter == ^uint32(0) {
		return errors.New("backup: 载荷块数超限")
	}
	nonce := chunkNonce(cw.nonce, cw.counter)
	sealed := cw.aead.Seal(nil, nonce, cw.buf, cw.aad)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(sealed)))
	if _, err := cw.w.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("backup: 写加密块长度失败: %w", err)
	}
	if _, err := cw.w.Write(sealed); err != nil {
		return fmt.Errorf("backup: 写加密块失败: %w", err)
	}
	cw.counter++
	cw.buf = cw.buf[:0]
	return nil
}

// Close 写出最后一块（可能不足 ChunkSize）。
func (cw *chunkWriter) Close() error {
	return cw.flushChunk()
}

// chunkReader 是 chunkWriter 的逆过程：读取长度前缀的 GCM 块并解密。
type chunkReader struct {
	r       io.Reader
	aead    cipher.AEAD
	nonce   []byte
	aad     []byte
	counter uint32
	plain   []byte
	done    bool
}

func newChunkReader(r io.Reader, aead cipher.AEAD, nonce, aad []byte) *chunkReader {
	return &chunkReader{r: r, aead: aead, nonce: nonce, aad: aad}
}

func (cr *chunkReader) Read(p []byte) (int, error) {
	for len(cr.plain) == 0 {
		if cr.done {
			return 0, io.EOF
		}
		if err := cr.readChunk(); err != nil {
			return 0, err
		}
	}
	n := copy(p, cr.plain)
	cr.plain = cr.plain[n:]
	return n, nil
}

func (cr *chunkReader) readChunk() error {
	var lenBuf [4]byte
	n, err := io.ReadFull(cr.r, lenBuf[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			cr.done = true
			return io.EOF
		}
		return fmt.Errorf("%w: 载荷被截断（缺少加密块长度）", ErrCorrupt)
	}
	sealedLen := binary.BigEndian.Uint32(lenBuf[:])
	if sealedLen < uint32(cr.aead.Overhead()) || sealedLen > uint32(ChunkSize+cr.aead.Overhead()) {
		return fmt.Errorf("%w: 加密块长度非法 %d", ErrCorrupt, sealedLen)
	}
	sealed := make([]byte, sealedLen)
	if _, err := io.ReadFull(cr.r, sealed); err != nil {
		return fmt.Errorf("%w: 载荷被截断（密文不完整）", ErrCorrupt)
	}
	nonce := chunkNonce(cr.nonce, cr.counter)
	plain, err := cr.aead.Open(nil, nonce, sealed, cr.aad)
	if err != nil {
		return fmt.Errorf("%w: 载荷认证失败（文件被篡改）", ErrCorrupt)
	}
	if cr.counter == ^uint32(0) {
		return fmt.Errorf("%w: 载荷块数超限", ErrCorrupt)
	}
	cr.counter++
	cr.plain = plain
	return nil
}

// ctxReader 在每次读取前检查 ctx，保证长耗时操作可被取消。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
