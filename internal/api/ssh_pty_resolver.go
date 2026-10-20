package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/maliang/leadstack/internal/credential"
	"github.com/maliang/leadstack/internal/sshx"
	"github.com/maliang/leadstack/internal/store"
)

// ResolveSSHPTYOptions 解析某台服务器的 SSH PTY 连接参数（终端用）。
//
// 与其它 SSH 通道共用同一套凭证解密与主机密钥策略；cmd 把它注入 term.NewSSHOpener。
func ResolveSSHPTYOptions(ctx context.Context, st *store.Store, cipher *credential.Cipher,
	knownHosts, serverID string) (sshx.PTYOptions, error) {
	if st == nil || cipher == nil {
		return sshx.PTYOptions{}, errors.New("终端通道依赖未就绪")
	}
	srv, err := st.GetServer(ctx, serverID)
	if err != nil {
		return sshx.PTYOptions{}, fmt.Errorf("查询服务器 %s 失败: %w", serverID, err)
	}
	if srv.CredentialID == nil || *srv.CredentialID == "" {
		return sshx.PTYOptions{}, errors.New("服务器未绑定 SSH 凭证，无法打开终端")
	}
	cred, err := st.GetCredential(ctx, *srv.CredentialID)
	if err != nil {
		return sshx.PTYOptions{}, fmt.Errorf("读取凭证失败: %w", err)
	}
	plain, err := cipher.Decrypt(cred.SecretEnc)
	if err != nil {
		return sshx.PTYOptions{}, fmt.Errorf("解密凭证失败: %w", err)
	}
	secret, err := credential.Unmarshal(plain)
	if err != nil {
		return sshx.PTYOptions{}, fmt.Errorf("解析凭证失败: %w", err)
	}
	policy := sshx.HostKeyPolicy(srv.HostKeyPolicy)
	if policy == "" {
		policy = sshx.PolicyAcceptNew
	}
	return sshx.PTYOptions{
		DialOptions: sshx.DialOptions{
			Host: srv.Host, Port: srv.Port,
			Auth: sshx.Auth{
				Username: secret.Username, Password: secret.Password,
				PrivateKey: secret.PrivateKey, Passphrase: secret.Passphrase,
			},
			Timeout:        sshExecDialTimeout,
			HostKeyPolicy:  policy,
			KnownHostsPath: knownHosts,
		},
	}, nil
}
