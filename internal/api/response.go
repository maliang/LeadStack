package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// 契约第 3 节定义的错误码。
const (
	codeUnauthorized    = "unauthorized"
	codeForbidden       = "forbidden"
	codeNotFound        = "not_found"
	codeConflict        = "conflict"
	codeInvalidArgument = "invalid_argument"
	codeInternal        = "internal"
	// codeSchemaTooNew 表示备份由更新版本创建（备份 §33 契约新增）。
	codeSchemaTooNew = "schema_too_new"
	// codeRemoteFailure 表示远端执行失败（agent/SSH/面板侧动作失败），固定 502。
	codeRemoteFailure = "remote_failure"
	// codeUnavailable 表示控制端自身能力未就绪（装配/配置问题），固定 503。
	// 与 remote_failure 刻意区分：本地没装配执行器不是目标机的错，
	// 否则用户会拿着「体检失败」去查目标机日志，一条线索都查不到。
	codeUnavailable = "unavailable"
)

// maxRequestBodyBytes 限制请求体大小，避免超大 JSON 打爆内存。
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// writeData 输出成功响应 {"data": ...}。
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

// errorRequestID 读取响应头里的请求号。
//
// 所有错误体（含带 details 的登录/限流形态）都从这一处取，保证 error.request_id
// 与响应头 X-Request-Id 逐字一致——运维拿请求号才能把用户报障对上日志。
func errorRequestID(w http.ResponseWriter) string {
	return strings.TrimSpace(w.Header().Get("X-Request-Id"))
}

// writeError 输出失败响应 {"error":{"code","message","request_id"}}。
//
// request_id 直接取响应头 X-Request-Id（由 middlewareRequestID 在请求入口写入，
// panic 兜底也走同一个头），因此不必改函数签名、也不必动 100+ 个调用点。
// 键始终存在：拿不到时为空字符串，前端可据此做降级。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":       code,
			"message":    message,
			"request_id": errorRequestID(w),
		},
	})
}

// writeUnavailable 输出 503：控制端本地能力缺失（执行器未装配等），不是远端故障。
//
// 刻意不套用「<动作>失败: 」的远端文案——那是 502 remote_failure 的语义。
func writeUnavailable(w http.ResponseWriter, message string) {
	writeError(w, http.StatusServiceUnavailable, codeUnavailable, message)
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
