package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// TestArtifactDownloadSignedURL 覆盖「目标机无 JWT 下载上传制品」这条链路：
// 带有效签名的请求 200；没有签名/签名错误 → 401（不能因为路由挪出鉴权组就变成公开下载）。
func TestArtifactDownloadSignedURL(t *testing.T) {
	t.Setenv("LEADSTACK_PUBLIC_URL", "http://127.0.0.1:18555")
	env := newTestEnv(t)
	ctx := context.Background()

	if err := os.MkdirAll(env.cfg.ArtifactsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	id := "art-signed"
	content := []byte("zip-bytes")
	if err := os.WriteFile(filepath.Join(env.cfg.ArtifactsDir(), artifactFileName(id)), content, 0o600); err != nil {
		t.Fatal(err)
	}
	art := &store.Artifact{ID: id, Kind: store.ArtifactKindUpload, Location: id, CreatedAt: store.NowMS(), SizeBytes: int64(len(content))}
	if err := env.st.CreateArtifact(ctx, art); err != nil {
		t.Fatal(err)
	}

	handler := env.api.Router()
	signed, err := SignedArtifactURL(ctx, env.st, id)
	if err != nil {
		t.Fatalf("生成签名 URL 失败: %v", err)
	}
	// 把绝对地址换成测试内路径
	path := signed[strings.Index(signed, "/api/v1/"):]
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("带签名的下载应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(content) {
		t.Fatalf("下载内容不一致: %q", rec.Body.String())
	}

	// 无签名 → 401
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+id+"/download", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("无签名应 401，实际 %d", rec2.Code)
	}

	// 篡改签名 / 过期 → 401
	u, _ := url.Parse(path)
	q := u.Query()
	q.Set("sig", "deadbeef")
	u.RawQuery = q.Encode()
	bad := u.String()
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, bad, nil))
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("错误签名应 401，实际 %d", rec3.Code)
	}
	q.Set("exp", "1")
	u.RawQuery = q.Encode()
	expired := u.String()
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, expired, nil))
	if rec4.Code != http.StatusUnauthorized {
		t.Fatalf("过期签名应 401，实际 %d", rec4.Code)
	}
}
