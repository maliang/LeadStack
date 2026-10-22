package api

import (
	"context"
	"net/http"

	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/maliang/leadstack/internal/auth"
	"github.com/maliang/leadstack/internal/store"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 制品下载签名：目标机在部署时用 curl 拉取「上传型制品」，它没有 JWT，
// 因此下载地址带一个短时效签名（HMAC-SHA256），由控制端在发布计划里生成。
//
// 设计取舍：
//   - 签名只绑定 artifactID + 过期时间，不绑定服务器（同一制品可能被多台机器同时拉取）；
//   - 过期时间默认 30 分钟（足够一次发布），控制端不需要维护会话；
//   - 密钥首次启动随机生成并落库（settings），重启后不变。
const (
	settingArtifactSignKey = "artifact.sign_key"
	artifactURLTTL         = 30 * time.Minute
)

// artifactSignKey 读取（或首次生成）制品下载签名密钥。
func (a *API) artifactSignKey(ctx context.Context) ([]byte, error) {
	return artifactSignKeyWith(ctx, a.st)
}

// artifactSignKeyWith 是包级实现（编排器与 API 共用）。
func artifactSignKeyWith(ctx context.Context, st *store.Store) ([]byte, error) {
	if raw, ok, err := st.GetSetting(ctx, settingArtifactSignKey); err != nil {
		return nil, err
	} else if ok && strings.TrimSpace(raw) != "" {
		key, derr := hex.DecodeString(strings.TrimSpace(raw))
		if derr == nil && len(key) >= 16 {
			return key, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成制品签名密钥失败: %w", err)
	}
	encoded := hex.EncodeToString(buf)
	if err := st.SetSetting(ctx, settingArtifactSignKey, encoded); err != nil {
		return nil, fmt.Errorf("保存制品签名密钥失败: %w", err)
	}
	return buf, nil
}

// artifactSignature 计算签名：HMAC(artifactID + "|" + exp)。
func artifactSignature(key []byte, artifactID string, exp int64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(artifactID + "|" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// signedArtifactURL 生成带签名的下载地址（供发布计划使用）。
func (a *API) signedArtifactURL(ctx context.Context, artifactID string) (string, error) {
	return SignedArtifactURL(ctx, a.st, artifactID)
}

// SignedArtifactURL 是包级实现（编排器与 API 共用）。
func SignedArtifactURL(ctx context.Context, st *store.Store, artifactID string) (string, error) {
	base, _, err := ResolvePublicURL(ctx, st)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("未配置控制端对外地址（设置 → 网络与端口），无法生成制品下载地址")
	}
	key, err := artifactSignKeyWith(ctx, st)
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(artifactURLTTL).Unix()
	u := fmt.Sprintf("%s/api/v1/artifacts/%s/download", strings.TrimSuffix(base, "/"), artifactID)
	return u + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + artifactSignature(key, artifactID, exp), nil
}

// verifyArtifactSignature 校验下载请求里的签名（过期或签名不符都拒绝）。
func (a *API) verifyArtifactSignature(ctx context.Context, artifactID string, q url.Values) bool {
	expRaw := q.Get("exp")
	sig := q.Get("sig")
	if expRaw == "" || sig == "" {
		return false
	}
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	key, err := a.artifactSignKey(ctx)
	if err != nil {
		return false
	}
	want := artifactSignature(key, artifactID, exp)
	return hmac.Equal([]byte(want), []byte(strings.ToLower(sig)))
}

// artifactAccessMiddleware 让「带有效签名」的请求直接放行，否则回到 JWT 权限校验。
func (a *API) artifactAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "artifactID")
		if id != "" && a.verifyArtifactSignature(r.Context(), id, r.URL.Query()) {
			next.ServeHTTP(w, r)
			return
		}
		// JWT 路径：先鉴权（填上下文），再校验 deploy.read。
		a.requireAuth(a.RequirePerm(auth.PermDeployRead)(next)).ServeHTTP(w, r)
	})
}
