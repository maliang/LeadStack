package api

import (
	"runtime"
	"strings"
	"testing"
)

// realReleaseAssets 是 v0.1.1 真实发行版返回的资产名与顺序（取自 GitHub API）。
//
// 注意：GitHub 按名字排序返回，`leadstack-agent_*` 排在 `leadstack_*` 之前——
// 这正是「按 os/arch 取第一个匹配」会误选 agent 包的原因（真实环境必然复现）。
var realReleaseAssets = []string{
	"checksums.txt",
	"leadstack-agent_0.1.1_darwin_amd64.tar.gz",
	"leadstack-agent_0.1.1_darwin_arm64.tar.gz",
	"leadstack-agent_0.1.1_linux_amd64.tar.gz",
	"leadstack-agent_0.1.1_linux_arm64.tar.gz",
	"leadstack-agent_0.1.1_windows_amd64.zip",
	"leadstack_0.1.1_darwin_amd64.tar.gz",
	"leadstack_0.1.1_darwin_arm64.tar.gz",
	"leadstack_0.1.1_linux_amd64.tar.gz",
	"leadstack_0.1.1_linux_arm64.tar.gz",
	"leadstack_0.1.1_windows_amd64.zip",
}

// TestClassifyAssetSkipsAgentArchives 校验只有控制端产物被当作升级源。
func TestClassifyAssetSkipsAgentArchives(t *testing.T) {
	kept := make([]string, 0, len(realReleaseAssets))
	for _, name := range realReleaseAssets {
		_, _, ok := classifyAsset(name)
		if ok {
			kept = append(kept, name)
		}
	}
	if len(kept) != 5 {
		t.Fatalf("应只保留 5 个控制端产物，实际 %d：%v", len(kept), kept)
	}
	for _, name := range kept {
		if !strings.HasPrefix(name, "leadstack_") {
			t.Fatalf("被保留的资产不是控制端产物：%s", name)
		}
	}
	if len(kept) > 0 {
		if osName, arch, _ := classifyAsset("leadstack-agent_0.1.1_linux_amd64.tar.gz"); osName != "" || arch != "" {
			t.Fatalf("agent 包不应被识别为升级资产（得到 %s/%s）", osName, arch)
		}
	}
}

// TestPickAssetPrefersControlPlane 校验为当前平台挑出的是控制端包（而非 agent 包）。
func TestPickAssetPrefersControlPlane(t *testing.T) {
	info := UpdateInfo{Latest: "v0.1.1", Assets: assetsFromNames(realReleaseAssets)}
	asset, err := (&API{}).pickAsset(info, "v0.1.1")
	if err != nil {
		t.Fatalf("挑选产物失败: %v", err)
	}
	wantPrefix := "leadstack_0.1.1_" + runtime.GOOS + "_" + runtime.GOARCH
	if !strings.HasPrefix(asset.Name, wantPrefix) {
		t.Fatalf("应挑控制端包 %s*，实际挑到 %s", wantPrefix, asset.Name)
	}
	if strings.Contains(asset.Name, "leadstack-agent_") {
		t.Fatalf("绝不能挑 agent 包：%s", asset.Name)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(asset.Name, ".zip") {
		t.Fatalf("Windows 应挑 zip：%s", asset.Name)
	}
	if runtime.GOOS != "windows" && !strings.HasSuffix(asset.Name, ".tar.gz") {
		t.Fatalf("类 Unix 应挑 tar.gz：%s", asset.Name)
	}
}

// assetsFromNames 用 classifyAsset 把资产名列表转成 UpdateInfo.Assets（与 refresh 同口径）。
func assetsFromNames(names []string) []UpdateAsset {
	out := make([]UpdateAsset, 0, len(names))
	for _, name := range names {
		if osName, arch, ok := classifyAsset(name); ok {
			out = append(out, UpdateAsset{OS: osName, Arch: arch, Name: name, URL: "https://example.invalid/" + name})
		}
	}
	return out
}
