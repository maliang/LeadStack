package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// streamChunkWriter 收集输出并在写入时实时回调；两个流共用回调锁保证串行。
type streamChunkWriter struct {
	stream string
	cb     func(stream, chunk string)
	cbMu   *sync.Mutex
	buf    bytes.Buffer
}

func (w *streamChunkWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if w.cb != nil && len(p) > 0 {
		chunk := string(p)
		w.cbMu.Lock()
		w.cb(w.stream, chunk)
		w.cbMu.Unlock()
	}
	return len(p), nil
}

func (w *streamChunkWriter) String() string {
	return w.buf.String()
}

// RunStream 在远端执行一条命令，并把 stdout/stderr 以「明文分块」实时回调给调用方。
//
// onChunk 的 stream 取值为 "stdout" 或 "stderr"；回调串行执行（不会并发重入），
// 但请保持回调轻量——它位于 SSH 读取路径上。
// 返回值语义与 Run 一致：命令非 0 退出返回 exitCode 且 err == nil；
// err 只表示传输层失败或 context 取消（此时返回 -1 与 ctx.Err()）。
func (c *Client) RunStream(ctx context.Context, cmd string, onChunk func(stream, chunk string)) (stdout, stderr string, exitCode int, err error) {
	if c == nil || c.client == nil {
		return "", "", -1, errors.New("sshx: 连接未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", -1, err
	}

	sess, err := c.client.NewSession()
	if err != nil {
		return "", "", -1, fmt.Errorf("sshx: 创建 SSH 会话失败: %w", err)
	}
	defer sess.Close()

	cbMu := &sync.Mutex{}
	outW := &streamChunkWriter{stream: "stdout", cb: onChunk, cbMu: cbMu}
	errW := &streamChunkWriter{stream: "stderr", cb: onChunk, cbMu: cbMu}
	sess.Stdout = outW
	sess.Stderr = errW

	done := make(chan error, 1)
	go func() {
		done <- sess.Run(cmd)
	}()

	select {
	case runErr := <-done:
		code, classErr := classifyExit(runErr)
		return outW.String(), errW.String(), code, classErr
	case <-ctx.Done():
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		return "", "", -1, ctx.Err()
	}
}
