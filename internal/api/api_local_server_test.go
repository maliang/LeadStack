package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/maliang/leadstack/internal/store"
)

// localServerResp 是 POST /api/v1/servers/local 的响应形状（只取断言需要的字段）。
type localServerResp struct {
	Data struct {
		Server struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Mode string `json:"mode"`
			Host string `json:"host"`
		} `json:"server"`
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Command   string `json:"command"`
		Created   bool   `json:"created"`
	} `json:"data"`
}

// postLocalServer 调一次接入本机接口并解析响应。
func postLocalServer(t *testing.T, env *testEnv, jwt string, body map[string]any) (int, localServerResp) {
	t.Helper()
	code, raw := env.request(http.MethodPost, "/api/v1/servers/local", jwt, body)
	var resp localServerResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("解析接入本机响应失败: %v，body=%s", err, raw)
	}
	return code, resp
}

// listServersAPI 读 GET /servers，返回服务器 id 列表（排在响应里的顺序）。
func listServersAPI(t *testing.T, env *testEnv, jwt string) []string {
	t.Helper()
	code, raw := env.request(http.MethodGet, "/api/v1/servers", jwt, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /servers 应 200，实际 %d：%s", code, raw)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析服务器列表失败: %v，body=%s", err, raw)
	}
	ids := make([]string, 0, len(out.Data))
	for _, s := range out.Data {
		ids = append(ids, s.ID)
	}
	return ids
}

// TestLocalServerIdempotent 首次接入新建（created=true），重放复用同一台（created=false）
// 并只重新签发令牌：服务器不重复落库、新令牌绑定同一服务器。
func TestLocalServerIdempotent(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()

	code, first := postLocalServer(t, env, jwt, map[string]any{"name": "幂等本机", "ttl_s": 600})
	if code != http.StatusCreated {
		t.Fatalf("首次接入应 201，实际 %d：%+v", code, first)
	}
	if !first.Data.Created {
		t.Fatalf("首次接入应返回 created=true")
	}
	if first.Data.Server.Mode != store.ModeAgent || first.Data.Server.Host != "127.0.0.1" {
		t.Fatalf("本机服务器应为 agent/127.0.0.1，实际 %+v", first.Data.Server)
	}
	if first.Data.Token == "" || !strings.Contains(first.Data.Command, "--token '"+first.Data.Token+"'") {
		t.Fatalf("安装命令应包含令牌: %s", first.Data.Command)
	}
	if ids := listServersAPI(t, env, jwt); len(ids) != 1 || ids[0] != first.Data.Server.ID {
		t.Fatalf("首次接入后 /servers 应只有 1 条本机服务器，实际 %v", ids)
	}

	code, second := postLocalServer(t, env, jwt, map[string]any{"name": "幂等本机", "ttl_s": 600})
	if code != http.StatusOK {
		t.Fatalf("重放接入应 200（复用），实际 %d：%+v", code, second)
	}
	if second.Data.Created {
		t.Fatalf("重放接入应返回 created=false")
	}
	if second.Data.Server.ID != first.Data.Server.ID {
		t.Fatalf("重放应复用同一台服务器：first=%s second=%s", first.Data.Server.ID, second.Data.Server.ID)
	}
	if second.Data.Token == "" || second.Data.Token == first.Data.Token {
		t.Fatalf("重放应重新签发令牌（旧令牌可留待过期，但不能复用响应）")
	}
	if ids := listServersAPI(t, env, jwt); len(ids) != 1 {
		t.Fatalf("重放后 /servers 仍应只有 1 条，实际 %v", ids)
	}

	tok, err := env.st.FindEnrollTokenByHash(ctx, hashEnrollToken(second.Data.Token), store.NowMS())
	if err != nil {
		t.Fatalf("重放签发的令牌应可用: %v", err)
	}
	if tok.ServerID == nil || *tok.ServerID != first.Data.Server.ID {
		t.Fatalf("重放签发的令牌应绑定原服务器 %s，实际 %+v", first.Data.Server.ID, tok.ServerID)
	}
}

// TestLocalServerReuseExisting 历史库已有本机 agent 服务器时直接复用：
// 优先有有效令牌/在线的那台；不新增行、不改名；非法 ttl 在写入前被拒。
func TestLocalServerReuseExisting(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	ctx := context.Background()
	nowMS := store.NowMS()
	seenAt := nowMS

	orphan := &store.Server{
		ID: "srv-local-orphan", Name: "历史重复本机", Host: "127.0.0.1", Port: 0,
		Mode: store.ModeAgent, Status: "offline", CreatedAt: nowMS - 5000,
	}
	inUse := &store.Server{
		ID: "srv-local-in-use", Name: "在用的本机", Host: "127.0.0.1", Port: 0,
		Mode: store.ModeAgent, Status: "online", LastSeenAt: &seenAt, CreatedAt: nowMS - 1000,
	}
	for _, srv := range []*store.Server{orphan, inUse} {
		if err := env.st.CreateServer(ctx, srv); err != nil {
			t.Fatalf("预置本机服务器失败: %v", err)
		}
	}

	// 非法 ttl 必须在任何写入之前被拒：此时库里仍是预置的 2 条。
	code, raw := env.request(http.MethodPost, "/api/v1/servers/local", jwt, map[string]any{"ttl_s": 10})
	if code != http.StatusBadRequest {
		t.Fatalf("非法 ttl 应 400，实际 %d：%s", code, raw)
	}
	if ids := listServersAPI(t, env, jwt); len(ids) != 2 {
		t.Fatalf("非法请求不应写库，/servers 应仍为 2 条，实际 %v", ids)
	}

	code, resp := postLocalServer(t, env, jwt, map[string]any{"name": "想改的名字", "ttl_s": 600})
	if code != http.StatusOK || resp.Data.Created {
		t.Fatalf("已有本机服务器时应复用（200/created=false），实际 %d created=%v", code, resp.Data.Created)
	}
	if resp.Data.Server.ID != inUse.ID {
		t.Fatalf("应优先复用有心跳的本机服务器 %s，实际 %s", inUse.ID, resp.Data.Server.ID)
	}
	if resp.Data.Server.Name != inUse.Name {
		t.Fatalf("复用不应改名：期望 %q，实际 %q", inUse.Name, resp.Data.Server.Name)
	}
	if ids := listServersAPI(t, env, jwt); len(ids) != 2 {
		t.Fatalf("复用不应新增服务器，/servers 应仍为 2 条，实际 %v", ids)
	}

	tok, err := env.st.FindEnrollTokenByHash(ctx, hashEnrollToken(resp.Data.Token), store.NowMS())
	if err != nil {
		t.Fatalf("复用签发的令牌应可用: %v", err)
	}
	if tok.ServerID == nil || *tok.ServerID != inUse.ID {
		t.Fatalf("令牌应绑定被复用的服务器 %s，实际 %+v", inUse.ID, tok.ServerID)
	}
}

// TestLocalServerConcurrentCreate 并发两次（这里放大到 8 次）接入只能建一台本机服务器。
func TestLocalServerConcurrentCreate(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	bodies := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]any{"name": "并发本机", "ttl_s": 600})
			req, err := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/servers/local", bytes.NewReader(payload))
			if err != nil {
				errs[i] = err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+jwt)
			resp, err := env.http.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				errs[i] = err
				return
			}
			codes[i] = resp.StatusCode
			bodies[i] = string(raw)
		}(i)
	}
	wg.Wait()

	var serverID string
	createdCount := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("并发请求 %d 失败: %v", i, errs[i])
		}
		var resp localServerResp
		if err := json.Unmarshal([]byte(bodies[i]), &resp); err != nil {
			t.Fatalf("并发请求 %d 响应无法解析: %v，body=%s", i, err, bodies[i])
		}
		if codes[i] != http.StatusCreated && codes[i] != http.StatusOK {
			t.Fatalf("并发请求 %d 应 200/201，实际 %d：%s", i, codes[i], bodies[i])
		}
		if resp.Data.Created {
			createdCount++
		}
		if serverID == "" {
			serverID = resp.Data.Server.ID
		} else if resp.Data.Server.ID != serverID {
			t.Fatalf("并发接入产生了不同服务器：%s vs %s", serverID, resp.Data.Server.ID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("并发接入应恰好新建 1 台，实际 created=true 出现 %d 次", createdCount)
	}
	if ids := listServersAPI(t, env, jwt); len(ids) != 1 || ids[0] != serverID {
		t.Fatalf("并发接入后 /servers 应只有 1 条，实际 %v", ids)
	}
}
