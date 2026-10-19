package alerting

import (
	"context"
	"errors"
	"sync"
)

// fakeStore 是 Store 的内存实现，用于单测。所有方法并发安全。
type fakeStore struct {
	mu     sync.Mutex
	rules  []Rule
	alerts map[string]*Alert // id -> alert
	events []fakeEvent

	listRulesErr   error
	findErr        error
	createErr      error
	updateErr      error
	resolveErr     error
	appendEventErr error

	// createCalls 统计 CreateAlert 被调用的次数（并发去重断言用）。
	createCalls int
	// findDelay 让 FindActiveAlert 在返回前故意放慢，放大并发竞争窗口。
	findDelay func()
}

type fakeEvent struct {
	AlertID string
	Kind    string
	TS      int64
	Detail  string
}

func newFakeStore(rules ...Rule) *fakeStore {
	return &fakeStore{rules: rules, alerts: make(map[string]*Alert)}
}

func (s *fakeStore) ListRules(ctx context.Context) ([]Rule, error) {
	if s.listRulesErr != nil {
		return nil, s.listRulesErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, len(s.rules))
	copy(out, s.rules)
	return out, nil
}

func (s *fakeStore) FindActiveAlert(ctx context.Context, dedupKey string) (*Alert, error) {
	if s.findDelay != nil {
		s.findDelay()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findErr != nil {
		return nil, s.findErr
	}
	for _, a := range s.alerts {
		if a.DedupKey != dedupKey {
			continue
		}
		if a.Status == StatusFiring || a.Status == StatusAcked {
			cp := *a
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) CreateAlert(ctx context.Context, a Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.createCalls++
	cp := a
	s.alerts[a.ID] = &cp
	return nil
}

func (s *fakeStore) UpdateAlert(ctx context.Context, a Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	if _, ok := s.alerts[a.ID]; !ok {
		return errors.New("fakeStore: 告警不存在: " + a.ID)
	}
	cp := a
	s.alerts[a.ID] = &cp
	return nil
}

func (s *fakeStore) ResolveAlert(ctx context.Context, id string, ts int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolveErr != nil {
		return s.resolveErr
	}
	a, ok := s.alerts[id]
	if !ok {
		return errors.New("fakeStore: 告警不存在: " + id)
	}
	a.Status = StatusResolved
	a.ResolvedAt = ts
	a.UpdatedAt = ts
	return nil
}

func (s *fakeStore) AppendAlertEvent(ctx context.Context, alertID, kind string, ts int64, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendEventErr != nil {
		return s.appendEventErr
	}
	s.events = append(s.events, fakeEvent{AlertID: alertID, Kind: kind, TS: ts, Detail: detail})
	return nil
}

// ---- 测试辅助查询 ----

func (s *fakeStore) alertCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}

func (s *fakeStore) allAlerts() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Alert, 0, len(s.alerts))
	for _, a := range s.alerts {
		out = append(out, *a)
	}
	return out
}

func (s *fakeStore) eventsOfKind(kind string) []fakeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fakeEvent
	for _, e := range s.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (s *fakeStore) eventsFor(alertID string) []fakeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fakeEvent
	for _, e := range s.events {
		if e.AlertID == alertID {
			out = append(out, e)
		}
	}
	return out
}

func (s *fakeStore) createCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCalls
}

func (s *fakeStore) setRules(rules ...Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
}

// fakeClock 是可注入的假时钟：测试只推进它，绝不 sleep。
type fakeClock struct {
	mu sync.Mutex
	ts int64 // Unix 毫秒
}

func newFakeClock(startMS int64) *fakeClock { return &fakeClock{ts: startMS} }

func (c *fakeClock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ts
}

func (c *fakeClock) Advance(d int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ts += d
}

func (c *fakeClock) Set(ts int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ts = ts
}

// notifyRecorder 记录 Notify 回调。
type notifyRecorder struct {
	mu    sync.Mutex
	calls []notifyCall
}

type notifyCall struct {
	Alert     Alert
	Channels  []string
	Recovered bool
}

func (r *notifyRecorder) fn(ctx context.Context, a Alert, channels []string, recovered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(channels))
	copy(cp, channels)
	r.calls = append(r.calls, notifyCall{Alert: a, Channels: cp, Recovered: recovered})
}

func (r *notifyRecorder) all() []notifyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]notifyCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *notifyRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *notifyRecorder) recoveredCalls() []notifyCall {
	var out []notifyCall
	for _, c := range r.all() {
		if c.Recovered {
			out = append(out, c)
		}
	}
	return out
}

func (r *notifyRecorder) firingCalls() []notifyCall {
	var out []notifyCall
	for _, c := range r.all() {
		if !c.Recovered {
			out = append(out, c)
		}
	}
	return out
}
