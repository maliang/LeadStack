package api

import (
	"context"

	"github.com/maliang/leadstack/internal/alerting"
	"github.com/maliang/leadstack/internal/notify"
)

// AlertEngine 是告警引擎的端口（*alerting.Engine 直接满足）。
//
// 抽成接口便于 API 层用替身做单测，也避免在 handler 里直接依赖引擎实现。
type AlertEngine interface {
	HandleEvent(ctx context.Context, ev alerting.Event) error
	EvaluateMetrics(ctx context.Context, samples []alerting.MetricSample) error
	Reload(ctx context.Context) error
}

// 编译期确认引擎实现。
var _ AlertEngine = (*alerting.Engine)(nil)

// Notifier 是通知渠道的端口（*NotifyAdapter 实现，内部包 notify.Client）。
type Notifier interface {
	Validate(ctx context.Context, kind string, config map[string]any) error
	Send(ctx context.Context, ch notify.Channel, msg notify.Message) error
}

// NotifyAdapter 把 internal/notify.Client 适配成 api.Notifier。
type NotifyAdapter struct {
	client *notify.Client
}

// 编译期确认接口实现。
var _ Notifier = (*NotifyAdapter)(nil)

// NewNotifyAdapter 包装通知客户端；client 为 nil 时返回 nil。
func NewNotifyAdapter(client *notify.Client) *NotifyAdapter {
	if client == nil {
		return nil
	}
	return &NotifyAdapter{client: client}
}

// Validate 校验渠道配置（契约第 19.2 节的各 Kind 必填项）。
func (a *NotifyAdapter) Validate(_ context.Context, kind string, config map[string]any) error {
	return a.client.Validate(notify.Channel{Kind: notify.Kind(kind), Config: config})
}

// Send 发送一条通知。
func (a *NotifyAdapter) Send(ctx context.Context, ch notify.Channel, msg notify.Message) error {
	return a.client.Send(ctx, ch, msg)
}
