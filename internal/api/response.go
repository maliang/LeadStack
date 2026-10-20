package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// 契约第 3 节定义的错误码。
const (
	codeUnauthorized    = "unauthorized"
	codeForbidden       = "forbidden"
	codeNotFound        = "not_found"
	codeConflict        = "conflict"
	codeInvalidArgument = "invalid_argument"
	codeInternal        = "internal"
	// codeSchemaTooNew 表示备份由更新版本创建（M4.5 §33 契约新增）。
	codeSchemaTooNew = "schema_too_new"
)

// maxRequestBodyBytes 限制请求体大小，避免超大 JSON 打爆内存。
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// writeData 输出成功响应 {"data": ...}。
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

// writeError 输出失败响应 {"error":{"code","message"}}。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// writeJSON 统一写 JSON 响应，写失败只记日志（此时响应头已发出，无法再改状态码）。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("写入 JSON 响应失败", "err", err)
	}
}

// decodeJSON 解析请求体；限制大小、拒绝多余 JSON 值。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("解析请求体 JSON: %w", err)
	}
	// 只允许一个 JSON 文档，防止请求体里夹带额外内容。
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("请求体必须是单个 JSON 对象")
	}
	return nil
}
