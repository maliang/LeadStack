package term

import (
	"context"
	"errors"

	"github.com/maliang/leadstack/internal/sshx"
)

// SSHOpenerOptions 是 SSHOpener 的依赖。
type SSHOpenerOptions struct {
	// Resolve 根据 serverID 解析 SSH 连接参数（由 backend 注入：查库 + 解密凭证 + 主机密钥策略）。
	Resolve func(ctx context.Context, serverID string) (sshx.PTYOptions, error)
}

// SSHOpener 用 sshx.PTY 实现 Opener（ssh 模式终端）。
type SSHOpener struct {
	resolve func(ctx context.Context, serverID string) (sshx.PTYOptions, error)
}

// NewSSHOpener 构造 SSHOpener。
func NewSSHOpener(opts SSHOpenerOptions) (*SSHOpener, error) {
	if opts.Resolve == nil {
		return nil, errors.New("term: SSHOpenerOptions.Resolve 不能为空")
	}
	return &SSHOpener{resolve: opts.Resolve}, nil
}

// Open 打开指定服务器的交互式终端。
func (o *SSHOpener) Open(ctx context.Context, serverID string) (Session, error) {
	if o == nil || o.resolve == nil {
		return nil, errors.New("term: SSHOpener 未初始化")
	}
	if serverID == "" {
		return nil, errors.New("term: serverID 不能为空")
	}
	opts, err := o.resolve(ctx, serverID)
	if err != nil {
		return nil, err
	}
	sess, err := sshx.PTY(ctx, opts)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

var (
	_ Opener  = (*SSHOpener)(nil)
	_ Session = (*sshx.PTYSession)(nil)
)
