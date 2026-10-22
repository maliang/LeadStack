// Package baota 是宝塔面板（BaoTa Panel）开放 API 的客户端。
//
// 用途：让 LeadStack 在「宝塔面板托管的机器」上完成**初始化部署**——
// 建站（或复用已有站点）、上传制品、解压到站点根目录、可选健康检查。
//
// 约定与限制（见 docs/ARCHITECTURE.md §57）：
//   - 面板需在「设置 → API 接口」开启，并把控制端出口 IP 加入白名单；
//   - 鉴权方式：`request_time` + `request_token = md5(md5(api_key) + request_time)`；
//   - 上传走 `files?action=SaveFileBody`（base64），单文件适合 ≤ 16MB 的制品；
//     更大的制品请改用 builtin（SSH/agent）通道，或先在面板文件管理里落盘；
//   - 本包只依赖标准库，便于独立单测与替换（版本字段差异集中在 endpoints 常量附近）。
package baota

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config 是面板连接配置（来自 leadstack 的 `baota` 类型凭据）。
type Config struct {
	// BaseURL 是面板地址，如 https://1.2.3.4:8888（可带子路径）。
	BaseURL string
	// APIKey 是面板「API 接口」里显示的密钥（api_sk）。
	APIKey string
	// InsecureSkipVerify 跳过 TLS 证书校验（面板多为自签证书）。
	InsecureSkipVerify bool
	// Timeout 是单次请求超时，默认 15s。
	Timeout time.Duration
}

// Site 是面板上的一个站点。
type Site struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Port   int    `json:"port"`
}

// AddSiteRequest 是新建站点的入参。
type AddSiteRequest struct {
	// Name 是站点名（同时作为默认域名占位，如 app.example.com 或 slug.local）。
	Name string
	// Path 是站点根目录，如 /www/wwwroot/app-slug。
	Path string
	// Port 是监听端口（静态站点默认 80）。
	Port int
	// Kind 是站点类型：static / php。
	Kind string
	// Note 是备注。
	Note string
}

// Client 是宝塔面板 API 客户端。
type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

// New 构造客户端并校验配置。
func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, errors.New("宝塔面板地址不能为空")
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("宝塔面板地址不合法: %s", cfg.BaseURL)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("宝塔面板 API 密钥不能为空")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   4,
	}
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 面板普遍自签，由用户显式开启
	}
	return &Client{
		base:   strings.TrimSuffix(base, "/"),
		apiKey: strings.TrimSpace(cfg.APIKey),
		http:   &http.Client{Timeout: timeout, Transport: transport},
	}, nil
}

// endpoints 集中管理面板接口路径，便于面板版本差异时只改这一处。
const (
	pathSystemTotal = "/system?action=GetSystemTotal"
	pathSiteList    = "/data?action=getData&table=sites"
	pathAddSite     = "/site?action=AddSite"
	pathSaveFile    = "/files?action=SaveFileBody"
	pathUnzip       = "/files?action=Unzip"
	pathZip         = "/files?action=Zip"
	pathDeleteFile  = "/files?action=DeleteFile"
)

// token 返回面板鉴权 token：md5(api_key)。
func (c *Client) token() string {
	sum := md5.Sum([]byte(c.apiKey))
	return hex.EncodeToString(sum[:])
}

// sign 计算请求签名：md5(md5(api_key) + request_time)。
func (c *Client) sign(requestTime string) string {
	sum := md5.Sum([]byte(c.token() + requestTime))
	return hex.EncodeToString(sum[:])
}

// post 发起一次带签名的表单请求，返回响应体。
func (c *Client) post(ctx context.Context, path string, form url.Values) ([]byte, error) {
	if form == nil {
		form = url.Values{}
	}
	requestTime := fmt.Sprintf("%d", time.Now().Unix())
	sum := c.sign(requestTime)
	form.Set("request_time", requestTime)
	form.Set("request_token", sum)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("构造宝塔请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "leadstack-baota/1")
	// 部分面板版本同时校验 Cookie 中的 request_token。
	req.AddCookie(&http.Cookie{Name: "request_token", Value: sum})

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求宝塔面板失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取宝塔响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("宝塔面板返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	// 面板对业务失败统一返回 {"status":false,"msg":"..."}，这里统一转成 error，
	// 避免调用方拿到「HTTP 200 但没做成」的假成功。
	var probe struct {
		Status *bool  `json:"status"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.Status != nil && !*probe.Status {
		msg := strings.TrimSpace(probe.Msg)
		if msg == "" {
			msg = "未知错误"
		}
		return nil, fmt.Errorf("面板返回失败: %s", msg)
	}
	return body, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Ping 调用 GetSystemTotal 验证地址、密钥与白名单是否可用。
func (c *Client) Ping(ctx context.Context) error {
	body, err := c.post(ctx, pathSystemTotal, nil)
	if err != nil {
		return err
	}
	var out struct {
		CPUNum   any    `json:"cpuNum"`
		MemTotal any    `json:"memTotal"`
		Status   *bool  `json:"status"`
		Msg      string `json:"msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("解析面板信息失败: %w（响应：%s）", err, snippet(body))
	}
	if out.Status != nil && !*out.Status {
		return fmt.Errorf("面板拒绝请求: %s", strings.TrimSpace(out.Msg))
	}
	if out.MemTotal == nil && out.CPUNum == nil && out.Status == nil {
		return fmt.Errorf("面板响应不是预期的系统信息（响应：%s）", snippet(body))
	}
	return nil
}

// ListSites 返回面板上的站点（最多 2000 个，够覆盖常规规模）。
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	form := url.Values{}
	form.Set("p", "1")
	form.Set("limit", "2000")
	form.Set("type", "-1")
	form.Set("order", "id desc")
	form.Set("tojs", "on")
	body, err := c.post(ctx, pathSiteList, form)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []Site `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析站点列表失败: %w（响应：%s）", err, snippet(body))
	}
	return out.Data, nil
}

// FindSite 按名称查找站点；不存在返回 (nil, nil)。
func (c *Client) FindSite(ctx context.Context, name string) (*Site, error) {
	sites, err := c.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	for i := range sites {
		if sites[i].Name == name {
			return &sites[i], nil
		}
	}
	return nil, nil
}

// EnsureSite 确保站点存在：已存在则直接返回（不覆盖），否则创建。
//
// 返回 created=true 表示本次新建。**绝不改写已有站点的目录/端口**，
// 避免把别人在面板里配好的站点搞坏（这是「接管已有项目」的安全底线）。
func (c *Client) EnsureSite(ctx context.Context, req AddSiteRequest) (*Site, bool, error) {
	name := strings.TrimSpace(req.Name)
	path := strings.TrimSpace(req.Path)
	if name == "" || path == "" {
		return nil, false, errors.New("站点名与站点目录不能为空")
	}
	existing, err := c.FindSite(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	port := req.Port
	if port <= 0 {
		port = 80
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = "static"
	}
	form := url.Values{}
	form.Set("webname", fmt.Sprintf(`{"domain":%q,"domainlist":[],"count":0}`, name))
	form.Set("path", path)
	form.Set("type", kind)
	form.Set("type_id", "0")
	form.Set("port", fmt.Sprintf("%d", port))
	form.Set("ps", strings.TrimSpace(req.Note))
	form.Set("version", "00")
	form.Set("ftp", "false")
	form.Set("sql", "false")
	form.Set("codeing", "utf8")
	form.Set("set_ssl", "0")
	if _, err := c.post(ctx, pathAddSite, form); err != nil {
		return nil, false, fmt.Errorf("创建站点 %s 失败: %w", name, err)
	}
	return &Site{Name: name, Path: path, Port: port, Status: "1"}, true, nil
}

// UploadFile 把内容以 base64 形式写入面板上的 path/filename（path 为目录）。
func (c *Client) UploadFile(ctx context.Context, dir, filename string, content []byte) error {
	if len(content) == 0 {
		return errors.New("上传内容为空")
	}
	dir = strings.TrimSpace(dir)
	filename = strings.TrimSpace(filename)
	if dir == "" || filename == "" {
		return errors.New("上传目录与文件名不能为空")
	}
	form := url.Values{}
	form.Set("path", dir)
	form.Set("filename", filename)
	form.Set("encoding", "base64")
	form.Set("data", base64.StdEncoding.EncodeToString(content))
	form.Set("f_size", fmt.Sprintf("%d", len(content)))
	if _, err := c.post(ctx, pathSaveFile, form); err != nil {
		return fmt.Errorf("上传 %s/%s 失败: %w", dir, filename, err)
	}
	return nil
}

// Unzip 把 zipPath 解压到 destDir（覆盖同名文件）。
func (c *Client) Unzip(ctx context.Context, zipPath, destDir string) error {
	form := url.Values{}
	form.Set("sfile", zipPath)
	form.Set("dfile", destDir)
	form.Set("type", "zip")
	form.Set("coding", "utf-8")
	form.Set("password", "")
	if _, err := c.post(ctx, pathUnzip, form); err != nil {
		return fmt.Errorf("解压 %s 到 %s 失败: %w", zipPath, destDir, err)
	}
	return nil
}

// ZipDir 把 dir 打成 zipPath（用于部署前备份站点目录）。
func (c *Client) ZipDir(ctx context.Context, dir, zipPath string) error {
	form := url.Values{}
	form.Set("sfile", dir)
	form.Set("dfile", zipPath)
	form.Set("type", "zip")
	form.Set("coding", "utf-8")
	if _, err := c.post(ctx, pathZip, form); err != nil {
		return fmt.Errorf("备份 %s 失败: %w", dir, err)
	}
	return nil
}

// DeleteFile 删除面板上的文件（best-effort 清理）。
func (c *Client) DeleteFile(ctx context.Context, path string) error {
	form := url.Values{}
	form.Set("path", path)
	if _, err := c.post(ctx, pathDeleteFile, form); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", path, err)
	}
	return nil
}
