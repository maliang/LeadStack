package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPOptions 是打开 SFTP 子系统的参数。
type SFTPOptions struct {
	DialOptions
}

// FileInfo 是一个远端文件/目录条目（字段与 REST 文件列表契约一致）。
type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"` // Unix 毫秒
	Mode    string `json:"mode"`
}

// SFTPSession 是一条 SFTP 会话。
type SFTPSession struct {
	client      *sftp.Client
	sshClient   *ssh.Client
	version     string
	fingerprint string

	closeOnce sync.Once
	closeErr  error
}

// SFTP 建立 SFTP 会话。
func SFTP(ctx context.Context, opts SFTPOptions) (*SFTPSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sshClient, version, fingerprint, err := dialSSH(ctx, opts.DialOptions)
	if err != nil {
		return nil, err
	}

	type openResult struct {
		client *sftp.Client
		err    error
	}
	resultCh := make(chan openResult, 1)
	go func() {
		c, err := sftp.NewClient(sshClient)
		resultCh <- openResult{client: c, err: err}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			_ = sshClient.Close()
			return nil, fmt.Errorf("sshx: 建立 SFTP 会话失败（远端可能未启用 sftp 子系统）: %w", res.err)
		}
		return &SFTPSession{
			client:      res.client,
			sshClient:   sshClient,
			version:     version,
			fingerprint: fingerprint,
		}, nil
	case <-ctx.Done():
		_ = sshClient.Close()
		select {
		case <-resultCh:
		case <-time.After(3 * time.Second):
		}
		return nil, ctx.Err()
	}
}

// List 列出目录内容（区分目录与文件，带大小与 mtime）。
func (s *SFTPSession) List(ctx context.Context, p string) ([]FileInfo, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	dir := normalizeRemoteDir(p)
	infos, err := s.client.ReadDirContext(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("sshx: SFTP 列目录 %s 失败: %w", dir, err)
	}
	out := make([]FileInfo, 0, len(infos))
	for _, fi := range infos {
		out = append(out, fileInfoFrom(dir, fi))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Stat 返回单个路径的信息。
func (s *SFTPSession) Stat(ctx context.Context, p string) (*FileInfo, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if p == "" {
		return nil, errors.New("sshx: SFTP Stat 路径不能为空")
	}
	fi, err := s.client.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("sshx: SFTP Stat %s 失败: %w", p, err)
	}
	info := fileInfoFrom("", fi)
	info.Path = p
	return &info, nil
}

// Open 以只读方式打开远端文件，供调用方流式下载。
func (s *SFTPSession) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if p == "" {
		return nil, errors.New("sshx: SFTP Open 路径不能为空")
	}
	f, err := s.client.Open(p)
	if err != nil {
		return nil, fmt.Errorf("sshx: SFTP 打开 %s 失败: %w", p, err)
	}
	return &ctxReadCloser{ctx: ctx, rc: f}, nil
}

// Read 读取整个文件（适合小文件；大文件请用 Open 流式读取）。
func (s *SFTPSession) Read(ctx context.Context, p string) ([]byte, error) {
	rc, err := s.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("sshx: SFTP 读取 %s 失败: %w", p, err)
	}
	return data, nil
}

// Write 流式写入远端文件；size < 0 表示未知大小。返回写入字节数。
//
// 失败时会尽力删除半成品文件，避免在目标机留下不完整文件。
func (s *SFTPSession) Write(ctx context.Context, p string, r io.Reader, size int64) (int64, error) {
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if p == "" {
		return 0, errors.New("sshx: SFTP Write 路径不能为空")
	}
	if r == nil {
		return 0, errors.New("sshx: SFTP Write 内容为空")
	}
	f, err := s.client.Create(p)
	if err != nil {
		return 0, fmt.Errorf("sshx: SFTP 创建 %s 失败: %w", p, err)
	}
	n, copyErr := io.Copy(&ctxWriter{ctx: ctx, w: f}, r)
	if copyErr == nil {
		copyErr = f.Close()
	} else {
		_ = f.Close()
	}
	if copyErr == nil && size >= 0 && n != size {
		copyErr = fmt.Errorf("sshx: SFTP 上传 %s 字节数不符（期望 %d，实际 %d）", p, size, n)
	}
	if copyErr != nil {
		_ = s.client.Remove(p)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
		return n, fmt.Errorf("sshx: SFTP 写入 %s 失败: %w", p, copyErr)
	}
	return n, nil
}

// Remove 删除文件或空目录。
func (s *SFTPSession) Remove(ctx context.Context, p string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if p == "" {
		return errors.New("sshx: SFTP Remove 路径不能为空")
	}
	fi, err := s.client.Stat(p)
	if err != nil {
		return fmt.Errorf("sshx: SFTP 删除前 Stat %s 失败: %w", p, err)
	}
	if fi.IsDir() {
		if err := s.client.RemoveDirectory(p); err != nil {
			return fmt.Errorf("sshx: SFTP 删除目录 %s 失败（目录可能非空）: %w", p, err)
		}
		return nil
	}
	if err := s.client.Remove(p); err != nil {
		return fmt.Errorf("sshx: SFTP 删除 %s 失败: %w", p, err)
	}
	return nil
}

// Mkdir 创建单层目录。
func (s *SFTPSession) Mkdir(ctx context.Context, p string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if p == "" {
		return errors.New("sshx: SFTP Mkdir 路径不能为空")
	}
	if err := s.client.Mkdir(p); err != nil {
		return fmt.Errorf("sshx: SFTP 创建目录 %s 失败: %w", p, err)
	}
	return nil
}

// MkdirAll 递归创建目录（扩展便利方法）。
func (s *SFTPSession) MkdirAll(ctx context.Context, p string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if p == "" {
		return errors.New("sshx: SFTP MkdirAll 路径不能为空")
	}
	if err := s.client.MkdirAll(p); err != nil {
		return fmt.Errorf("sshx: SFTP 递归创建目录 %s 失败: %w", p, err)
	}
	return nil
}

// Close 关闭 SFTP 会话（同时关闭底层 SSH 连接）；可重复调用。
func (s *SFTPSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.client != nil {
			s.closeErr = s.client.Close()
		}
		if s.sshClient != nil {
			if err := s.sshClient.Close(); err != nil && s.closeErr == nil && !errors.Is(err, os.ErrClosed) {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

// ServerVersion 返回远端 SSH 版本串。
func (s *SFTPSession) ServerVersion() string {
	if s == nil {
		return ""
	}
	return s.version
}

// HostKeyFingerprint 返回主机密钥指纹 "SHA256:<base64>"。
func (s *SFTPSession) HostKeyFingerprint() string {
	if s == nil {
		return ""
	}
	return s.fingerprint
}

func (s *SFTPSession) ready(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("sshx: SFTP 会话未初始化或已关闭")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// normalizeRemoteDir 把空路径归一化为当前目录（远端永远是 POSIX 路径）。
func normalizeRemoteDir(p string) string {
	if p == "" {
		return "."
	}
	return p
}

// fileInfoFrom 把 os.FileInfo 转成契约结构；dir 非空时拼接完整路径。
func fileInfoFrom(dir string, fi os.FileInfo) FileInfo {
	name := fi.Name()
	full := name
	if dir != "" && dir != "." {
		full = path.Join(dir, name)
	}
	return FileInfo{
		Name:    name,
		Path:    full,
		IsDir:   fi.IsDir(),
		Size:    fi.Size(),
		ModTime: fi.ModTime().UnixMilli(),
		Mode:    fi.Mode().String(),
	}
}

// ctxReadCloser 让流式下载可被 ctx 取消（取消时关闭底层文件）。
type ctxReadCloser struct {
	ctx context.Context
	rc  io.ReadCloser
}

func (r *ctxReadCloser) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		_ = r.rc.Close()
		return 0, err
	}
	return r.rc.Read(p)
}

func (r *ctxReadCloser) Close() error {
	return r.rc.Close()
}

// ctxWriter 让流式上传可被 ctx 取消。
type ctxWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w *ctxWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}
