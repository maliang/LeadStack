// Package devssh 是测试专用 SSH 服务，仅用于开发与测试，不参与发布构建。
//
// 契约见 docs/ARCHITECTURE.md 第 8.4 节（已冻结，签名不得改名）。
// 本机没有 sshd，它是 SSH 路径唯一可控的验收端点。
//
// 安全提示：它使用固定账号密码、每次启动随机生成主机密钥、默认把命令交给本机
// `sh -c` 执行。**绝不可用于生产环境。**
package devssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"

	gliderssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

const (
	defaultAddr     = "127.0.0.1:0"
	defaultUsername = "devuser"
	defaultPassword = "devpass"
)

// Config 是 devssh 的启动参数。
type Config struct {
	Addr          string                                                 // 如 127.0.0.1:0，传 0 由系统分配
	Username      string                                                 // 默认 devuser
	Password      string                                                 // 默认 devpass
	AuthorizedKey []byte                                                 // 允许的客户端公钥（authorized_keys 行）
	Executor      func(cmd string) (stdout, stderr string, exitCode int) // 默认：本机真实执行 sh -c
}

// Server 是一个运行中的测试 SSH 服务。
type Server struct {
	ln     net.Listener
	srv    *gliderssh.Server
	exec   func(cmd string) (string, string, int)
	addr   string
	mu     sync.Mutex
	closed bool
}

// Start 启动测试 SSH 服务。
func Start(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.Username == "" {
		cfg.Username = defaultUsername
	}
	if cfg.Password == "" {
		cfg.Password = defaultPassword
	}

	var authorized []gliderssh.PublicKey
	if len(bytes.TrimSpace(cfg.AuthorizedKey)) > 0 {
		keys, err := parseAuthorizedKeys(cfg.AuthorizedKey)
		if err != nil {
			return nil, err
		}
		authorized = keys
	}

	// 每次启动生成独立的主机密钥：便于测试“主机密钥变化”场景。
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devssh: 生成主机密钥失败: %w", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		return nil, fmt.Errorf("devssh: 构造主机密钥签名器失败: %w", err)
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("devssh: 监听 %s 失败: %w", cfg.Addr, err)
	}

	s := &Server{
		ln:   ln,
		addr: ln.Addr().String(),
		exec: cfg.Executor,
	}

	srv := &gliderssh.Server{
		Handler: s.handleSession,
	}
	srv.AddHostKey(hostSigner)

	// SFTP 子系统：测试专用，直接暴露本机文件系统（DEV-ONLY）。
	srv.SubsystemHandlers = map[string]gliderssh.SubsystemHandler{
		"sftp": func(sess gliderssh.Session) {
			server, err := sftp.NewServer(sess)
			if err != nil {
				_, _ = io.WriteString(sess.Stderr(), "devssh: 启动 SFTP 子系统失败: "+err.Error()+"\n")
				return
			}
			defer server.Close()
			if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
				_, _ = io.WriteString(sess.Stderr(), "devssh: SFTP 会话结束: "+err.Error()+"\n")
			}
		},
	}

	// 固定账号密码：未知用户一律拒绝。
	srv.PasswordHandler = func(ctx gliderssh.Context, password string) bool {
		if ctx.User() != cfg.Username {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(password), []byte(cfg.Password)) == 1
	}
	// 公钥认证（与密码认证取或）。未配置公钥时保持 handler 为 nil。
	if len(authorized) > 0 {
		srv.PublicKeyHandler = func(ctx gliderssh.Context, key gliderssh.PublicKey) bool {
			if ctx.User() != cfg.Username {
				return false
			}
			for _, want := range authorized {
				if bytes.Equal(want.Marshal(), key.Marshal()) {
					return true
				}
			}
			return false
		}
	}
	s.srv = srv

	go func() {
		// Serve 在 Close 后返回，错误已通过关闭流程体现，这里无需额外处理。
		_ = srv.Serve(ln)
	}()

	return s, nil
}

// parseAuthorizedKeys 解析 authorized_keys 格式内容（忽略空行与注释）。
func parseAuthorizedKeys(b []byte) ([]gliderssh.PublicKey, error) {
	var keys []gliderssh.PublicKey
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _, _, err := gliderssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("devssh: 解析公钥失败: %w", err)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("devssh: AuthorizedKey 中没有可用的公钥")
	}
	return keys, nil
}

// handleSession 把 exec 请求交给 Executor（默认本机 sh -c）执行并回传真实结果；
// 请求了 pty 的会话则交给真实伪终端 shell（DEV-ONLY）。
func (s *Server) handleSession(sess gliderssh.Session) {
	if ptyReq, winCh, isPty := sess.Pty(); isPty {
		servePTYShell(sess, ptyReq, winCh)
		return
	}
	cmd := sess.RawCommand()
	if strings.TrimSpace(cmd) == "" {
		// 不支持交互式 shell：测试端点只服务 exec 请求。
		_, _ = io.WriteString(sess.Stderr(), "devssh: interactive shell 未实现，请使用 exec（如 ssh host 'echo ok'）\n")
		_ = sess.Exit(1)
		return
	}

	var stdout, stderr string
	var exitCode int
	if s.exec != nil {
		stdout, stderr, exitCode = s.exec(cmd)
	} else {
		stdout, stderr, exitCode = execLocal(sess.Context(), cmd)
	}

	if stdout != "" {
		_, _ = io.WriteString(sess, stdout)
	}
	if stderr != "" {
		_, _ = io.WriteString(sess.Stderr(), stderr)
	}
	_ = sess.Exit(exitCode)
}

// execLocal 在本机真实执行 sh -c 命令。
func execLocal(ctx context.Context, cmd string) (stdout, stderr string, exitCode int) {
	// 会话断开时取消本机进程，避免 devssh 泄露子进程。
	if ctx == nil {
		ctx = context.Background()
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	if err == nil {
		return outBuf.String(), errBuf.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode()
	}
	errBuf.WriteString(err.Error())
	errBuf.WriteByte('\n')
	return outBuf.String(), errBuf.String(), 127
}

// Addr 返回实际监听地址（如 127.0.0.1:54321）。
func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

// Close 关闭服务；可重复调用。
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.srv.Close()
	if lnErr := s.ln.Close(); lnErr != nil && !errors.Is(lnErr, net.ErrClosed) && err == nil {
		err = lnErr
	}
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}
