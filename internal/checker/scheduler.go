package checker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// minInterval 是调度间隔下限（契约要求最小 10s）。做成变量只为测试可缩短；
// 生产路径不允许外部修改。
var minInterval = time.Duration(MinIntervalS) * time.Second

// reloadInterval 是 Wake 之外的兜底重读周期：即使 backend 漏调 Wake，
// 增删的检测项也会在一个周期内生效。
var reloadInterval = 30 * time.Second

// shutdownGrace 是 Start 等待在飞探测结束的最长时间，避免关机被慢探测卡死。
var shutdownGrace = 5 * time.Second

type targetKey struct {
	checkID  string
	serverID string
}

func (k targetKey) String() string { return k.checkID + "/" + k.serverID }

// targetState 是一个 (check, server) 目标的调度与状态机状态。
type targetState struct {
	key targetKey

	// specMu 保护 spec/mode/interval：Wake 重排与探测 goroutine 并发访问。
	specMu   sync.RWMutex
	spec     Spec
	mode     string
	interval time.Duration

	// probeMu 保证同一目标不重叠执行（定时探测与手动 RunOnce 互斥）。
	probeMu sync.Mutex

	// 状态机字段只在持有 probeMu 时访问。
	fails     int  // 连续失败次数
	up        bool // 判定后的状态（含 fail_threshold 阈值）
	since     int64
	hasResult bool

	cancel context.CancelFunc
	done   chan struct{}
}

func (st *targetState) getSpec() (Spec, time.Duration) {
	st.specMu.RLock()
	defer st.specMu.RUnlock()
	return st.spec, st.interval
}

func (st *targetState) setSpec(spec Spec, mode string) {
	st.specMu.Lock()
	defer st.specMu.Unlock()
	st.spec = spec
	st.mode = mode
	st.interval = intervalOf(spec)
}

// apply 推进状态机，返回（判定后的状态, 是否翻转, 当前状态的起始时间）。
//
// 状态口径（docs/ARCHITECTURE.md 第 15 节）：
// down = 最近一次失败且连续失败次数 ≥ fail_threshold，否则 up。
// 首次有结果时只建立基线（unknown → up/down 不算翻转），避免误触发 告警 告警。
func (st *targetState) apply(r Result, failThreshold int) (stateOK bool, flipped bool, since int64) {
	if r.OK {
		st.fails = 0
		stateOK = true
	} else {
		st.fails++
		stateOK = st.fails < failThreshold
	}
	if !st.hasResult {
		st.hasResult = true
		st.up = stateOK
		st.since = r.TS
		return stateOK, false, st.since
	}
	if stateOK != st.up {
		st.up = stateOK
		st.since = r.TS
		return stateOK, true, st.since
	}
	return stateOK, false, st.since
}

// adhocLock 用于给「尚未装载进调度表」的目标做 RunOnce 互斥。
type adhocLock struct {
	mu   sync.Mutex
	refs int
}

// Scheduler 按 (check, server) 目标维护独立 ticker 执行探测。
//
// 行为约定：
//   - 每个目标一个 goroutine + 独立 ticker，同一个目标串行执行，天然不重叠；
//   - 全局并发上限 Deps.Concurrency（默认 8），信号量限制同时在跑的探测数；
//   - Wake() 非阻塞地要求重读目标列表（检测项增删改后调用）；
//   - Start(ctx) 阻塞直到 ctx 取消，取消后等待在飞探测结束再返回；
//   - OnStateChange 只在状态真正翻转（up→down / down→up）时调用。
type Scheduler struct {
	deps Deps
	sem  chan struct{}

	wakeCh chan struct{}

	mu      sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	targets map[targetKey]*targetState
	adhoc   map[targetKey]*adhocLock
	wg      sync.WaitGroup
}

// NewScheduler 构造调度器；Concurrency <= 0 时取 DefaultConcurrency。
func NewScheduler(d Deps) *Scheduler {
	if d.Concurrency <= 0 {
		d.Concurrency = DefaultConcurrency
	}
	return &Scheduler{
		deps:    d,
		sem:     make(chan struct{}, d.Concurrency),
		wakeCh:  make(chan struct{}, 1),
		targets: make(map[targetKey]*targetState),
		adhoc:   make(map[targetKey]*adhocLock),
	}
}

// Start 阻塞直到 ctx 取消并优雅退出。
//
// 重复调用 Start 只有第一次生效（第二次立即返回）。
func (s *Scheduler) Start(ctx context.Context) {
	if !s.begin(ctx) {
		return
	}
	defer s.stop()

	s.reload(ctx)
	ticker := time.NewTicker(reloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wakeCh:
			s.reload(s.ctx)
		case <-ticker.C:
			s.reload(s.ctx)
		}
	}
}

// Wake 让调度器重读目标列表并重排（检测项增删改后调用）。非阻塞。
func (s *Scheduler) Wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// RunOnce 手动「立即执行」一次，用于 POST /checks/{id}/run。
//
// 与定时探测共用同一把目标锁，因此不会与调度器里的探测重叠执行。
// 目标不在调度表中时（例如检测项刚被创建、尚未 Wake），会直接从 Store 查一次；
// 查不到则返回 OK=false 而不是 panic。
func (s *Scheduler) RunOnce(ctx context.Context, checkID, serverID string) Result {
	key := targetKey{checkID: checkID, serverID: serverID}

	s.mu.Lock()
	st, known := s.targets[key]
	var adhoc *adhocLock
	if known {
		st.probeMu.Lock()
	} else {
		adhoc = s.adhoc[key]
		if adhoc == nil {
			adhoc = &adhocLock{}
			s.adhoc[key] = adhoc
		}
		adhoc.refs++
	}
	s.mu.Unlock()

	if known {
		defer st.probeMu.Unlock()
	} else {
		adhoc.mu.Lock()
		defer func() {
			adhoc.mu.Unlock()
			s.mu.Lock()
			adhoc.refs--
			if adhoc.refs <= 0 {
				delete(s.adhoc, key)
			}
			s.mu.Unlock()
		}()
	}

	if !known {
		t, found := s.lookupTarget(ctx, key)
		if !found {
			return failResult(0, "未找到检测目标: check=%s server=%s", checkID, serverID)
		}
		st = &targetState{
			key:      key,
			spec:     t.Spec,
			mode:     t.Mode,
			interval: intervalOf(t.Spec),
			up:       true,
		}
	}

	spec, _ := st.getSpec()
	res := s.probeWithLimit(ctx, spec, key.serverID)
	if ctx.Err() != nil {
		// 手动执行被取消：返回结果但不落库，避免把取消写成一次失败。
		return res
	}
	s.record(ctx, st, res)
	return res
}

// begin 标记启动并派生调度用的 ctx。
func (s *Scheduler) begin(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return false
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	return true
}

// stop 取消全部目标并等待在飞探测结束（最多 shutdownGrace）。
func (s *Scheduler) stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
	}

	s.mu.Lock()
	for k, st := range s.targets {
		delete(s.targets, k)
		st.cancel()
	}
	s.started = false
	s.ctx, s.cancel = nil, nil
	s.mu.Unlock()
}

// reload 重读目标列表并重排：
//   - 消失的目标：停掉；
//   - 间隔变化：重启 ticker；
//   - 参数/超时变化：就地更新 spec（保留状态机，避免误报翻转）；
//   - 新增目标：起 goroutine。
//
// Store 报错时保留现有目标，等下一次 Wake/兜底重读，避免一次抖动把全部检测停掉。
func (s *Scheduler) reload(ctx context.Context) {
	if s.deps.Store == nil {
		return
	}
	list, err := s.deps.Store.ListEnabledTargets(ctx)
	if err != nil {
		return
	}
	wanted := make(map[targetKey]Target, len(list))
	for _, t := range list {
		if t.CheckID == "" || t.ServerID == "" {
			continue
		}
		wanted[targetKey{checkID: t.CheckID, serverID: t.ServerID}] = t
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, st := range s.targets {
		nt, ok := wanted[k]
		if !ok {
			s.stopTargetLocked(k, st)
			continue
		}
		if intervalOf(nt.Spec) != st.interval {
			s.stopTargetLocked(k, st)
			s.startTargetLocked(k, nt)
			continue
		}
		st.setSpec(nt.Spec, nt.Mode)
	}
	for k, t := range wanted {
		if _, ok := s.targets[k]; ok {
			continue
		}
		s.startTargetLocked(k, t)
	}
}

func (s *Scheduler) startTargetLocked(k targetKey, t Target) {
	tctx, cancel := context.WithCancel(s.ctx)
	st := &targetState{
		key:      k,
		spec:     t.Spec,
		mode:     t.Mode,
		interval: intervalOf(t.Spec),
		up:       true,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	s.targets[k] = st
	s.wg.Add(1)
	go s.runTarget(tctx, st)
}

func (s *Scheduler) stopTargetLocked(k targetKey, st *targetState) {
	delete(s.targets, k)
	st.cancel()
}

// runTarget 是单个目标的调度循环：立即跑一次，然后按间隔重复。
func (s *Scheduler) runTarget(ctx context.Context, st *targetState) {
	defer s.wg.Done()
	defer close(st.done)

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.execute(ctx, st)
		_, interval := st.getSpec()
		timer.Reset(interval)
	}
}

// execute 在单个目标上跑一次探测并落库（持有 probeMu，故不与 RunOnce 重叠）。
func (s *Scheduler) execute(ctx context.Context, st *targetState) {
	st.probeMu.Lock()
	defer st.probeMu.Unlock()

	spec, _ := st.getSpec()
	res := s.probeWithLimit(ctx, spec, st.key.serverID)
	if ctx.Err() != nil {
		return // 关机中：不要把取消写成一次失败
	}
	s.record(ctx, st, res)
}

// probeWithLimit 在全球并发上限内执行一次探测。
func (s *Scheduler) probeWithLimit(ctx context.Context, spec Spec, serverID string) Result {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return failResult(0, "探测被取消: %s", ctx.Err().Error())
	}
	return Probe(ctx, spec, s.deps.Exec, serverID)
}

// record 落库结果与状态，并在状态翻转时回调 OnStateChange。
func (s *Scheduler) record(ctx context.Context, st *targetState, r Result) {
	spec, _ := st.getSpec()
	stateOK, flipped, since := st.apply(r, failThresholdOf(spec))

	if s.deps.Store == nil {
		if flipped && s.deps.OnStateChange != nil {
			s.deps.OnStateChange(st.key.checkID, st.key.serverID, stateOK, since)
		}
		return
	}
	// 落库失败不阻塞调度：保留内存状态，下一轮再写。
	_ = s.deps.Store.SaveResult(ctx, st.key.checkID, st.key.serverID, r)
	_ = s.deps.Store.UpdateState(ctx, st.key.checkID, st.key.serverID, stateOK, r.TS)

	if flipped && s.deps.OnStateChange != nil {
		s.deps.OnStateChange(st.key.checkID, st.key.serverID, stateOK, since)
	}
}

// lookupTarget 从 Store 现查一个目标（可能尚未装载进调度表）。
func (s *Scheduler) lookupTarget(ctx context.Context, key targetKey) (Target, bool) {
	if s.deps.Store == nil {
		return Target{}, false
	}
	list, err := s.deps.Store.ListEnabledTargets(ctx)
	if err != nil {
		return Target{}, false
	}
	for _, t := range list {
		if t.CheckID == key.checkID && t.ServerID == key.serverID {
			return t, true
		}
	}
	return Target{}, false
}

// intervalOf 返回规范化的调度间隔（下限 minInterval）。
func intervalOf(spec Spec) time.Duration {
	iv := time.Duration(spec.IntervalS) * time.Second
	if iv < minInterval {
		iv = minInterval
	}
	return iv
}

// String 便于日志/调试。
func (s *Scheduler) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("checker.Scheduler{targets:%d, concurrency:%d}", len(s.targets), cap(s.sem))
}
