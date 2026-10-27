package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/sshx"
)

// 文件接口参数（契约第 41 节）。
const (
	fileListTimeoutS  = 30
	fileTransferTimeS = 120
	filePathMaxRunes  = 4096
	fileDownloadMaxB  = 64 << 20 // 64 MiB 上限（防误点超大文件）
)

// filesPayload 是目录列表响应。
type filesPayload struct {
	Path    string          `json:"path"`
	Entries []sshx.FileInfo `json:"entries"`
}

// uploadPayload 是上传响应。
type uploadPayload struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// sanitizeRemotePath 校验远端路径：必须是绝对路径、不含 NUL、不允许 ".." 段。
func sanitizeRemotePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("path 不能为空")
	}
	if len(p) > filePathMaxRunes {
		return "", errors.New("path 过长")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path 含非法字符")
	}
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("path 必须是绝对路径")
	}
	// 拒绝任何 ".." 段（路径穿越）。
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("path 不允许包含 .. 段")
		}
	}
	return path.Clean(p), nil
}

// fileOps 返回某台服务器的文件操作通道（ssh → SFTP；agent → job.exec + base64）。
func (a *API) fileList(ctx context.Context, serverID, remotePath string) ([]sshx.FileInfo, error) {
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.Mode == "ssh" && a.sftp != nil {
		sess, err := a.sftp.OpenSFTP(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("打开 SFTP 会话失败: %w", err)
		}
		defer func() { _ = sess.Close() }()
		return sess.List(ctx, remotePath)
	}
	if a.remoteExec == nil {
		return nil, errNoRemoteExecutor
	}
	// agent 通道：ls -la 解析（best-effort）。
	return a.agentListFiles(ctx, serverID, remotePath)
}

// agentListFiles 用 ls -la 列出目录（agent 通道）。
func (a *API) agentListFiles(ctx context.Context, serverID, remotePath string) ([]sshx.FileInfo, error) {
	out, stderr, code, err := a.remoteExec.Exec(ctx, serverID, "ls -la -- "+shellQuote(remotePath), fileListTimeoutS)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("列出目录失败: %v %s", err, strings.TrimSpace(stderr))
	}
	entries := make([]sshx.FileInfo, 0, 32)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || strings.HasPrefix(line, "total ") {
			continue
		}
		name := strings.Join(fields[8:], " ")
		if name == "." || name == ".." {
			continue
		}
		isDir := strings.HasPrefix(fields[0], "d")
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		entries = append(entries, sshx.FileInfo{
			Name: name, Path: path.Join(remotePath, name), IsDir: isDir,
			Size: size, Mode: fields[0],
		})
	}
	return entries, nil
}

// shellQuote 对 shell 参数做最小转义（单引号包裹）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// handleListFiles 处理 GET /api/v1/servers/{id}/files?path=。
func (a *API) handleListFiles(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	remotePath, err := sanitizeRemotePath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if a.remoteExec == nil && a.sftp == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "远程执行通道未启用")
		return
	}
	entries, err := a.fileList(r.Context(), serverID, remotePath)
	if err != nil {
		a.writeRemoteError(w, r, err, "列出目录")
		return
	}
	if entries == nil {
		entries = []sshx.FileInfo{}
	}
	writeData(w, http.StatusOK, filesPayload{Path: remotePath, Entries: entries})
}

// handleDownloadFile 处理 GET /api/v1/servers/{id}/files/download?path=。
func (a *API) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	remotePath, err := sanitizeRemotePath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if a.sftp == nil && a.remoteExec == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "文件通道未启用（SFTP 与远程执行均不可用）")
		return
	}

	var reader io.ReadCloser
	if srv.Mode == "ssh" && a.sftp != nil {
		sess, serr := a.sftp.OpenSFTP(ctx, serverID)
		if serr != nil {
			a.writeRemoteError(w, r, serr, "打开 SFTP 会话")
			return
		}
		defer func() { _ = sess.Close() }()
		reader, err = sess.Open(ctx, remotePath)
		if err != nil {
			a.writeRemoteError(w, r, err, "打开远端文件")
			return
		}
	} else {
		// agent 通道：base64 读取（受大小上限约束）。
		out, stderr, code, rerr := a.remoteExec.Exec(ctx, serverID,
			"base64 -w0 < "+shellQuote(remotePath)+" 2>/dev/null || base64 < "+shellQuote(remotePath),
			fileTransferTimeS)
		if rerr != nil || code != 0 {
			a.writeRemoteError(w, r, fmt.Errorf("读取远端文件失败: %v %s", rerr, strings.TrimSpace(stderr)), "下载文件")
			return
		}
		decoded, derr := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(out), ""))
		if derr != nil {
			a.writeRemoteError(w, r, fmt.Errorf("解码文件内容失败: %w", derr), "下载文件")
			return
		}
		reader = io.NopCloser(strings.NewReader(string(decoded)))
	}
	defer func() {
		if cerr := reader.Close(); cerr != nil {
			a.logger.Debug("关闭远端文件失败", "err", cerr)
		}
	}()

	name := path.Base(remotePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, io.LimitReader(reader, fileDownloadMaxB)); err != nil {
		a.logger.Warn("下载远端文件中途失败", "server_id", serverID, "path", remotePath, "err", err)
	}
}

// handleUploadFile 处理 POST /api/v1/servers/{id}/files/upload（multipart: path + file）。
func (a *API) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	maxBytes := int64(a.cfg.FileMaxMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, codeInvalidArgument,
				fmt.Sprintf("文件超过 %d MiB 上限", a.cfg.FileMaxMB))
			return
		}
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "解析 multipart 失败: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if rerr := r.MultipartForm.RemoveAll(); rerr != nil {
				a.logger.Debug("清理上传临时文件失败", "err", rerr)
			}
		}
	}()

	remotePath, err := sanitizeRemotePath(r.FormValue("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少 file 字段")
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			a.logger.Debug("关闭上传文件失败", "err", cerr)
		}
	}()

	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if a.sftp == nil && a.remoteExec == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "文件通道未启用（SFTP 与远程执行均不可用）")
		return
	}

	var written int64
	if srv.Mode == "ssh" && a.sftp != nil {
		sess, serr := a.sftp.OpenSFTP(ctx, serverID)
		if serr != nil {
			a.writeRemoteError(w, r, serr, "打开 SFTP 会话")
			return
		}
		defer func() { _ = sess.Close() }()
		written, err = sess.Write(ctx, remotePath, file, header.Size)
		if err != nil {
			a.writeRemoteError(w, r, err, "写入远端文件")
			return
		}
	} else {
		raw, rerr := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if rerr != nil {
			a.internalError(w, r, fmt.Errorf("读取上传内容: %w", rerr))
			return
		}
		if int64(len(raw)) > maxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, codeInvalidArgument,
				fmt.Sprintf("文件超过 %d MiB 上限", a.cfg.FileMaxMB))
			return
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		cmd := fmt.Sprintf("printf %%s %s | base64 -d > %s", shellQuote(encoded), shellQuote(remotePath))
		_, stderr, code, xerr := a.remoteExec.Exec(ctx, serverID, cmd, fileTransferTimeS)
		if xerr != nil || code != 0 {
			a.writeRemoteError(w, r, fmt.Errorf("写入远端文件失败: %v %s", xerr, strings.TrimSpace(stderr)), "上传文件")
			return
		}
		written = int64(len(raw))
	}

	a.logger.Info("文件已上传", "server_id", serverID, "path", remotePath, "bytes", written)
	writeData(w, http.StatusOK, uploadPayload{OK: true, Path: remotePath, Bytes: written})
}

// handleDeleteFile 处理 DELETE /api/v1/servers/{id}/files?path=。
func (a *API) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	remotePath, err := sanitizeRemotePath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	ctx := r.Context()
	srv, err := a.st.GetServer(ctx, serverID)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	if a.sftp == nil && a.remoteExec == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "文件通道未启用（SFTP 与远程执行均不可用）")
		return
	}
	if srv.Mode == "ssh" && a.sftp != nil {
		sess, serr := a.sftp.OpenSFTP(ctx, serverID)
		if serr != nil {
			a.writeRemoteError(w, r, serr, "打开 SFTP 会话")
			return
		}
		defer func() { _ = sess.Close() }()
		if err := sess.Remove(ctx, remotePath); err != nil {
			a.writeRemoteError(w, r, err, "删除远端文件")
			return
		}
	} else {
		_, stderr, code, xerr := a.remoteExec.Exec(ctx, serverID, "rm -f -- "+shellQuote(remotePath), fileListTimeoutS)
		if xerr != nil || code != 0 {
			a.writeRemoteError(w, r, fmt.Errorf("删除远端文件失败: %v %s", xerr, strings.TrimSpace(stderr)), "删除文件")
			return
		}
	}
	a.logger.Info("文件已删除", "server_id", serverID, "path", remotePath)
	writeData(w, http.StatusOK, map[string]any{"ok": true, "path": remotePath})
}

// writeRemoteError 把远端执行错误映射成 502 + remote_failure（远端问题，可重试/需排查对端）。
//
// 与「本机程序内部错误（500 internal）」区分开：message 保留「<动作>失败: <原因>」模板，
// 原因里是底层错误串，不含任何凭据/密钥。
func (a *API) writeRemoteError(w http.ResponseWriter, r *http.Request, err error, action string) {
	if err == nil {
		return
	}
	// 运维操作默认按依赖不可用处理；真正的程序内部错误极少见。
	a.logger.Warn("远程操作失败", "action", action, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, codeRemoteFailure, fmt.Sprintf("%s失败: %v", action, err))
}
