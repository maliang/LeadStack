package api

import (
	"context"

	"github.com/maliang/leadstack/internal/notify"
)

// ChannelProber 是「测试发送」接口的可选诊断能力：用真实端点探测可达性。
//
// 单独成接口（不并入 Notifier），这样只实现 Notifier 的替身不会因缺 Probe 而编译失败。
type ChannelProber interface {
	Probe(ctx context.Context, ch notify.Channel) (notify.ProbeResult, error)
}

// 编译期确认适配器具备探测能力。
var _ ChannelProber = (*NotifyAdapter)(nil)

// Probe 用真实端点做一次只读探测：发一个中性探针消息，服务商任意应答（含 4xx）都算「可达」。
func (a *NotifyAdapter) Probe(ctx context.Context, ch notify.Channel) (notify.ProbeResult, error) {
	return a.client.Probe(ctx, ch)
}

// channelProbeInfo 是测试发送响应里新增的 probe 字段（既有字段保持不变）。
type channelProbeInfo struct {
	Reachable    bool   `json:"reachable"`
	StatusCode   int    `json:"status_code,omitempty"`
	ProviderCode string `json:"provider_code,omitempty"`
	Snippet      string `json:"snippet,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorKind    string `json:"error_kind,omitempty"`
	ErrorLabel   string `json:"error_label,omitempty"`
}

// probeAlertChannel 做一次真实端点探测并转换为响应结构。
//
// 探测失败返回带分类的说明，而不是让整个测试接口失败。
func (a *API) probeAlertChannel(ctx context.Context, ch notify.Channel) *channelProbeInfo {
	prober, ok := a.notifier.(ChannelProber)
	if !ok {
		return nil
	}
	res, err := prober.Probe(ctx, ch)
	info := &channelProbeInfo{Reachable: res.Reachable}
	if err != nil {
		info.Error = err.Error()
		if se, ok := notify.AsSendError(err); ok {
			info.ErrorKind = string(se.Kind)
			info.ErrorLabel = se.Kind.Label()
		}
		return info
	}
	info.StatusCode = res.StatusCode
	info.ProviderCode = res.ProviderCode
	info.Snippet = res.ResponseSnippet
	return info
}
