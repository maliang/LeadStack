package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestServerStatusEndpointReturnsLightweightSnapshots(t *testing.T) {
	env := newTestEnv(t)
	jwt := env.login()
	srv := env.createServerVia(jwt, map[string]any{
		"name": "status-node", "host": "10.0.0.88", "mode": "ssh",
	})

	status, raw := env.request(http.MethodGet, "/api/v1/servers/status", jwt, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /servers/status 期望 200，实际 %d body=%s", status, raw)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析状态响应失败: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0]["id"] != srv.ID {
		t.Fatalf("状态快照不符: %+v", out.Data)
	}
	if _, ok := out.Data[0]["tags"]; ok {
		t.Fatal("轻量状态接口不应返回 tags")
	}
	if _, ok := out.Data[0]["host_info"]; ok {
		t.Fatal("轻量状态接口不应返回 host_info")
	}
	if out.Data[0]["status"] == "" {
		t.Fatal("状态不能为空")
	}
}
