//go:build !unix

package devssh

import (
	"io"

	gliderssh "github.com/gliderlabs/ssh"
)

// servePTYShell 在非 unix 平台返回明确的“不支持”提示（devssh 仅用于开发测试）。
func servePTYShell(sess gliderssh.Session, _ gliderssh.Pty, _ <-chan gliderssh.Window) {
	_, _ = io.WriteString(sess.Stderr(), "devssh: 当前平台不支持 PTY shell（DEV-ONLY 测试端点）\n")
	_ = sess.Exit(1)
}
