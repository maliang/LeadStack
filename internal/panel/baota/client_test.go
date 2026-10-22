package baota

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockPanel 是一个最小的宝塔面板替身：校验签名并记录收到的请求。
type mockPanel struct {
	apiKey  string
	sites   []Site
	calls   []string
	forms   map[string]map[string]string
	failMsg string
}

func newMockPanel(apiKey string) *mockPanel {
	return &mockPanel{apiKey: apiKey, forms: map[string]map[string]string{}}
}

func sign(apiKey, requestTime string) string {
	sum := md5.Sum([]byte(apiKey))
	token := hex.EncodeToString(sum[:])
	s2 := md5.Sum([]byte(token + requestTime))
	return hex.EncodeToString(s2[:])
}

func (m *mockPanel) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		rt := r.PostFormValue("request_time")
		got := r.PostFormValue("request_token")
		if got != sign(m.apiKey, rt) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "msg": "签名校验失败"})
			return
		}
		path := r.URL.Path
		m.calls = append(m.calls, path+"?"+r.URL.RawQuery)
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostFormValue(k)
		}
		m.forms[r.URL.Path+"?"+r.URL.RawQuery] = form

		switch {
		case strings.Contains(r.URL.Path, "/system"):
			_ = json.NewEncoder(w).Encode(map[string]any{"cpuNum": 4, "memTotal": 8 << 30})
		case strings.Contains(r.URL.Path, "/data"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": m.sites})
		case strings.Contains(r.URL.Path, "/site"):
			if m.failMsg != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "msg": m.failMsg})
				return
			}
			name := ""
			var wn struct {
				Domain string `json:"domain"`
			}
			if err := json.Unmarshal([]byte(r.PostFormValue("webname")), &wn); err == nil {
				name = wn.Domain
			}
			m.sites = append(m.sites, Site{Name: name, Path: r.PostFormValue("path"), Status: "1"})
			_ = json.NewEncoder(w).Encode(map[string]any{"siteStatus": true})
		case strings.Contains(r.URL.Path, "/files"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
		default:
			http.NotFound(w, r)
		}
	})
}

func TestClientSignsRequestsAndPings(t *testing.T) {
	mock := newMockPanel("test-api-key")
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, APIKey: "test-api-key"})
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	if len(mock.calls) != 1 || !strings.Contains(mock.calls[0], "action=GetSystemTotal") {
		t.Fatalf("应调用 GetSystemTotal，实际 %v", mock.calls)
	}
	// 错误密钥 → 签名校验失败应报错
	bad, _ := New(Config{BaseURL: srv.URL, APIKey: "wrong-key"})
	if err := bad.Ping(context.Background()); err == nil {
		t.Fatal("错误密钥应返回错误")
	}
}

func TestEnsureSiteReusesExisting(t *testing.T) {
	mock := newMockPanel("k")
	mock.sites = []Site{{Name: "app.example.com", Path: "/www/wwwroot/app"}}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, APIKey: "k"})

	site, created, err := c.EnsureSite(context.Background(), AddSiteRequest{
		Name: "app.example.com", Path: "/www/wwwroot/app", Port: 8080, Kind: "static",
	})
	if err != nil {
		t.Fatalf("EnsureSite 失败: %v", err)
	}
	if created {
		t.Fatal("已存在的站点不应被重复创建")
	}
	if site.Path != "/www/wwwroot/app" {
		t.Fatalf("应返回已有站点信息，实际 %+v", site)
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "AddSite") {
			t.Fatalf("不应调用 AddSite：%v", mock.calls)
		}
	}
}

func TestEnsureSiteCreatesWithExpectedParams(t *testing.T) {
	mock := newMockPanel("k")
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, APIKey: "k"})

	_, created, err := c.EnsureSite(context.Background(), AddSiteRequest{
		Name: "shop.example.com", Path: "/www/wwwroot/shop", Port: 3000, Kind: "static", Note: "LeadStack",
	})
	if err != nil || !created {
		t.Fatalf("应新建站点: created=%v err=%v", created, err)
	}
	var form map[string]string
	for k, v := range mock.forms {
		if strings.Contains(k, "AddSite") {
			form = v
		}
	}
	if form == nil {
		t.Fatalf("未调用 AddSite: %v", mock.calls)
	}
	if form["path"] != "/www/wwwroot/shop" || form["port"] != "3000" || form["type"] != "static" {
		t.Fatalf("AddSite 参数不符: %+v", form)
	}
	if !strings.Contains(form["webname"], "shop.example.com") {
		t.Fatalf("webname 应包含域名: %s", form["webname"])
	}
}

func TestUploadUnzipAndBackupParams(t *testing.T) {
	mock := newMockPanel("k")
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, APIKey: "k"})
	ctx := context.Background()

	content := []byte("zip-bytes")
	if err := c.UploadFile(ctx, "/www/wwwroot/app", "release.zip", content); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if err := c.Unzip(ctx, "/www/wwwroot/app/release.zip", "/www/wwwroot/app"); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if err := c.ZipDir(ctx, "/www/wwwroot/app", "/www/wwwroot/app/backup.zip"); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	var upload, unzip, zip map[string]string
	for k, v := range mock.forms {
		switch {
		case strings.Contains(k, "SaveFileBody"):
			upload = v
		case strings.Contains(k, "Unzip"):
			unzip = v
		case strings.Contains(k, "Zip"):
			zip = v
		}
	}
	if upload == nil || upload["encoding"] != "base64" || upload["filename"] != "release.zip" {
		t.Fatalf("上传参数不符: %+v", upload)
	}
	raw, err := base64.StdEncoding.DecodeString(upload["data"])
	if err != nil || string(raw) != "zip-bytes" {
		t.Fatalf("上传内容不符: %v %q", err, string(raw))
	}
	if unzip == nil || unzip["sfile"] != "/www/wwwroot/app/release.zip" || unzip["dfile"] != "/www/wwwroot/app" || unzip["type"] != "zip" {
		t.Fatalf("解压参数不符: %+v", unzip)
	}
	if zip == nil || zip["sfile"] != "/www/wwwroot/app" || zip["dfile"] != "/www/wwwroot/app/backup.zip" {
		t.Fatalf("备份参数不符: %+v", zip)
	}
}

func TestPanelErrorSurfaces(t *testing.T) {
	mock := newMockPanel("k")
	mock.failMsg = "站点已存在"
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, APIKey: "k"})
	_, _, err := c.EnsureSite(context.Background(), AddSiteRequest{Name: "x.example.com", Path: "/www/wwwroot/x"})
	if err == nil || !strings.Contains(err.Error(), "站点已存在") {
		t.Fatalf("面板错误应透传，实际 %v", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("空地址应报错")
	}
	if _, err := New(Config{BaseURL: "1.2.3.4:8888"}); err == nil {
		t.Fatal("缺密钥应报错")
	}
	c, err := New(Config{BaseURL: "1.2.3.4:8888", APIKey: "k"})
	if err != nil {
		t.Fatalf("裸主机应自动补 https: %v", err)
	}
	if !strings.HasPrefix(c.base, "https://") {
		t.Fatalf("应补全为 https://，实际 %s", c.base)
	}
}
