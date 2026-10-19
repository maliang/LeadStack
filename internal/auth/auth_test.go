package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maliang/leadstack/internal/store"
)

// TestHashAndVerifyPassword 校验 bcrypt 哈希与校验的往返。
func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("S3cret-密码")
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}
	if hash == "S3cret-密码" {
		t.Fatal("哈希不应等于明文")
	}
	if !VerifyPassword(hash, "S3cret-密码") {
		t.Fatal("正确密码应校验通过")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("错误密码不应校验通过")
	}
	if VerifyPassword("", "S3cret-密码") {
		t.Fatal("空哈希不应校验通过")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("空密码应报错")
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); err == nil {
		t.Fatal("超长密码应报错")
	}
}

// TestGeneratePassword 校验随机密码的长度与字符集。
func TestGeneratePassword(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 16; i++ {
		pw, err := GeneratePassword(20)
		if err != nil {
			t.Fatalf("生成随机密码失败: %v", err)
		}
		if len(pw) != 20 {
			t.Fatalf("密码长度期望 20，实际 %d", len(pw))
		}
		for _, r := range pw {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("密码含字母表外字符 %q: %s", r, pw)
			}
		}
		if _, dup := seen[pw]; dup {
			t.Fatalf("随机密码出现重复: %s", pw)
		}
		seen[pw] = struct{}{}
	}
}

// TestJWTSignAndParse 校验 JWT 的签发、解析与各类非法情况。
func TestJWTSignAndParse(t *testing.T) {
	secret := bytes.Repeat([]byte("s"), 48)
	m, err := NewManager(secret, time.Hour)
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}

	token, expiresAt, err := m.Sign("user-1", "admin", "admin")
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("过期时间应晚于当前时间: %v", expiresAt)
	}

	claims, err := m.Parse(token)
	if err != nil {
		t.Fatalf("解析令牌失败: %v", err)
	}
	if claims.Subject != "user-1" || claims.Username != "admin" || claims.Role != "admin" {
		t.Fatalf("令牌载荷不符: %+v", claims)
	}

	if _, err := m.Parse(token + "x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("被篡改的令牌应返回 ErrInvalidToken，实际 %v", err)
	}
	if _, err := m.Parse(""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("空令牌应返回 ErrInvalidToken，实际 %v", err)
	}

	other, err := NewManager(bytes.Repeat([]byte("t"), 48), time.Hour)
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	if _, err := other.Parse(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("换密钥后应校验失败，实际 %v", err)
	}

	expired, err := NewManager(secret, time.Millisecond)
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	shortToken, _, err := expired.Sign("user-1", "admin", "admin")
	if err != nil {
		t.Fatalf("签发短效令牌失败: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := expired.Parse(shortToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("过期令牌应返回 ErrInvalidToken，实际 %v", err)
	}

	if _, err := NewManager([]byte("short"), time.Hour); err == nil {
		t.Fatal("过短的密钥应报错")
	}
}

// TestRequireAuthMiddleware 校验 Bearer 中间件的行为。
func TestRequireAuthMiddleware(t *testing.T) {
	m, err := NewManager(bytes.Repeat([]byte("s"), 48), time.Hour)
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	token, _, err := m.Sign("user-1", "admin", "admin")
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}

	handler := m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromRequest(r)
		if !ok {
			t.Error("handler 中应能取到 claims")
		}
		if claims.Role != "admin" {
			t.Errorf("角色期望 admin，实际 %q", claims.Role)
		}
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"无认证头", "", http.StatusUnauthorized},
		{"格式错误", "Token abc", http.StatusUnauthorized},
		{"非法令牌", "Bearer not-a-jwt", http.StatusUnauthorized},
		{"合法令牌", "Bearer " + token, http.StatusOK},
		{"大小写不敏感", "bearer " + token, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("状态码期望 %d，实际 %d（body=%s）", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRequireRoleMiddleware 校验 RBAC 中间件。
func TestRequireRoleMiddleware(t *testing.T) {
	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 未经过 RequireAuth：按 401 处理。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少凭证期望 401，实际 %d", rec.Code)
	}

	// 角色不匹配：403。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithClaims(req.Context(), &Claims{Username: "u", Role: "viewer"}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("角色不符期望 403，实际 %d", rec.Code)
	}

	// 角色匹配：200。
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithClaims(req.Context(), &Claims{Username: "u", Role: "admin"}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("角色匹配期望 200，实际 %d", rec.Code)
	}
}

// TestLoadOrCreateSecret 校验 JWT 密钥持久化：首次生成、之后复用。
func TestLoadOrCreateSecret(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("关闭测试数据库失败: %v", cerr)
		}
	}()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	first, err := LoadOrCreateSecret(ctx, st)
	if err != nil {
		t.Fatalf("首次加载密钥失败: %v", err)
	}
	if len(first) < 32 {
		t.Fatalf("密钥长度不足 32 字节: %d", len(first))
	}
	second, err := LoadOrCreateSecret(ctx, st)
	if err != nil {
		t.Fatalf("二次加载密钥失败: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("密钥应持久化并在重启后保持不变")
	}
}
