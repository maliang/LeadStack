package checker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stateChange struct {
	checkID  string
	serverID string
	ok       bool
	since    int64
}

func scriptTarget(checkID, serverID, script string, params map[string]any) Target {
	return Target{
		CheckID:  checkID,
		ServerID: serverID,
		Mode:     "agent",
		Spec: Spec{
			ID:        checkID,
			Name:      checkID,
			Kind:      KindScript,
			Target:    script,
			IntervalS: 0, // 0 表示用调度器下限（测试里被缩短）
			TimeoutS:  5,
			Params:    params,
		},
	}
}

func TestSchedulerRunsTargetsAndShutsDownGracefully(t *testing.T) {
	withFastInterval(t, 20*time.Millisecond)

	store := &fakeStore{}
	store.setTargets(scriptTarget("c1", "s1", "echo ok", nil))
	exec := &fakeExecutor{}

	s := NewScheduler(Deps{Store: store, Exec: exec, Concurrency: 4})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	if !waitFor(3*time.Second, func() bool { return store.resultCount() >= 3 }) {
		t.Fatalf("调度器未按间隔产生结果，实际 %d 条", store.resultCount())
	}
	if store.stateCount() < 3 {
		t.Fatalf("每次探测都应写入状态，实际 %d 条", store.stateCount())
	}
	for _, r := range store.resultsForServer("s1") {
		if r.TS == 0 {
			t.Fatalf("落库结果必须带 TS: %+v", r)
		}
	}
	if _, ok := store.lastState(); !ok {
		t.Fatalf("应有状态记录")
	} else if st, _ := store.lastState(); st.checkID != "c1" || st.serverID != "s1" {
		t.Fatalf("状态记录的目标不对: %+v", st)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Start 未在 ctx 取消后退出")
	}
}

func TestSchedulerSameTargetNeverOverlaps(t *testing.T) {
	withFastInterval(t, 15*time.Millisecond)

	store := &fakeStore{}
	store.setTargets(scriptTarget("c1", "s1", "slow", nil))
	exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
		time.Sleep(60 * time.Millisecond) // 比间隔长，若会重叠则峰值 > 1
		return "", "", 0, nil
	}}

	s := NewScheduler(Deps{Store: store, Exec: exec, Concurrency: 4})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	if !waitFor(3*time.Second, func() bool { return exec.callCount() >= 3 }) {
		t.Fatalf("探测次数不足: %d", exec.callCount())
	}
	if peak := exec.peakInflight(); peak != 1 {
		t.Fatalf("同一目标不得重叠执行，峰值并发=%d", peak)
	}
	cancel()
	<-done
}

func TestSchedulerRespectsConcurrencyLimit(t *testing.T) {
	// 目标间隔设得远大于执行时间，避免调度器自身的排队影响观测。
	withFastInterval(t, 500*time.Millisecond)

	store := &fakeStore{}
	for i := 0; i < 8; i++ {
		store.addTarget(scriptTarget("c"+string(rune('a'+i)), "s"+string(rune('a'+i)), "slow", nil))
	}
	exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
		time.Sleep(50 * time.Millisecond)
		return "", "", 0, nil
	}}

	s := NewScheduler(Deps{Store: store, Exec: exec, Concurrency: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	if !waitFor(3*time.Second, func() bool { return exec.callCount() >= 8 }) {
		t.Fatalf("8 个目标都应至少探测一次，实际 %d", exec.callCount())
	}
	if peak := exec.peakInflight(); peak > 2 {
		t.Fatalf("并发上限失效：峰值=%d > 2", peak)
	}
	cancel()
	<-done
}

func TestSchedulerOnStateChangeOnlyOnFlip(t *testing.T) {
	withFastInterval(t, 15*time.Millisecond)

	store := &fakeStore{}
	// fail_threshold=1：一次失败即 down，便于快速验证翻转语义。
	store.setTargets(scriptTarget("c1", "s1", "probe", map[string]any{"fail_threshold": 1}))

	var calls int32
	exec := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
		n := atomic.AddInt32(&calls, 1)
		switch {
		case n == 1 || n >= 4: // 1: 首次成功；4 起恢复
			return "ok", "", 0, nil
		default: // 2、3：连续失败
			return "", "boom", 1, nil
		}
	}}

	var mu sync.Mutex
	var changes []stateChange
	s := NewScheduler(Deps{
		Store: store,
		Exec:  exec,
		OnStateChange: func(checkID, serverID string, ok bool, since int64) {
			mu.Lock()
			defer mu.Unlock()
			changes = append(changes, stateChange{checkID: checkID, serverID: serverID, ok: ok, since: since})
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	if !waitFor(3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(changes) >= 2
	}) {
		mu.Lock()
		got := append([]stateChange(nil), changes...)
		mu.Unlock()
		t.Fatalf("未观察到状态翻转，已收到 %+v", got)
	}

	mu.Lock()
	snapshot := append([]stateChange(nil), changes...)
	mu.Unlock()
	if snapshot[0].ok || snapshot[0].checkID != "c1" || snapshot[0].serverID != "s1" {
		t.Fatalf("第一次翻转应为 down(ok=false): %+v", snapshot[0])
	}
	if !snapshot[1].ok {
		t.Fatalf("第二次翻转应为 up(ok=true): %+v", snapshot[1])
	}
	if snapshot[1].since < snapshot[0].since {
		t.Fatalf("since 应单调不减: %+v", snapshot)
	}

	// 3、4、5 次结果里不应再有额外翻转（第 3 次仍是失败但状态已是 down）。
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	got := len(changes)
	mu.Unlock()
	if got != 2 {
		mu.Lock()
		all := append([]stateChange(nil), changes...)
		mu.Unlock()
		t.Fatalf("状态未翻转时不得回调 OnStateChange，收到 %d 次: %+v", got, all)
	}
	cancel()
	<-done
}

func TestSchedulerWakePicksUpNewTarget(t *testing.T) {
	withFastInterval(t, 20*time.Millisecond)

	store := &fakeStore{}
	store.setTargets(scriptTarget("c1", "s1", "echo a", nil))
	exec := &fakeExecutor{}

	s := NewScheduler(Deps{Store: store, Exec: exec})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	if !waitFor(2*time.Second, func() bool { return len(store.resultsForServer("s1")) > 0 }) {
		t.Fatalf("初始目标未被执行")
	}

	store.addTarget(scriptTarget("c2", "s2", "echo b", nil))
	s.Wake()
	if !waitFor(2*time.Second, func() bool { return len(store.resultsForServer("s2")) > 0 }) {
		t.Fatalf("Wake 之后新目标未被执行")
	}

	// 移除目标后不应再产生新结果。
	store.setTargets(scriptTarget("c1", "s1", "echo a", nil))
	s.Wake()
	time.Sleep(150 * time.Millisecond)
	before := len(store.resultsForServer("s2"))
	time.Sleep(150 * time.Millisecond)
	after := len(store.resultsForServer("s2"))
	if after != before {
		t.Fatalf("已移除的目标仍在执行: %d -> %d", before, after)
	}
	cancel()
	<-done
}

func TestSchedulerKeepsTargetsWhenStoreListFails(t *testing.T) {
	withFastInterval(t, 20*time.Millisecond)

	store := &fakeStore{}
	store.setTargets(scriptTarget("c1", "s1", "echo a", nil))
	exec := &fakeExecutor{}
	s := NewScheduler(Deps{Store: store, Exec: exec})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()
	if !waitFor(2*time.Second, func() bool { return store.resultCount() >= 1 }) {
		t.Fatalf("初始目标未被执行")
	}

	store.mu.Lock()
	store.listErr = errors.New("db is down")
	store.mu.Unlock()

	before := store.resultCount()
	if !waitFor(2*time.Second, func() bool { return store.resultCount() > before }) {
		t.Fatalf("Store 列表报错时不应把已有目标停掉")
	}
	cancel()
	<-done
}

func TestSchedulerRunOnce(t *testing.T) {
	withFastInterval(t, time.Hour) // RunOnce 不依赖 ticker

	store := &fakeStore{}
	store.setTargets(scriptTarget("c1", "s1", "echo once", nil))
	exec := &fakeExecutor{}
	s := NewScheduler(Deps{Store: store, Exec: exec})

	// 调度器未启动也应能手动执行
	r := s.RunOnce(context.Background(), "c1", "s1")
	if !r.OK {
		t.Fatalf("RunOnce 应成功: %+v", r)
	}
	if store.resultCount() != 1 || store.stateCount() != 1 {
		t.Fatalf("RunOnce 应落库结果与状态: results=%d states=%d", store.resultCount(), store.stateCount())
	}

	r2 := s.RunOnce(context.Background(), "nope", "nope")
	if r2.OK || !containsAll(r2.Message, "未找到检测目标") {
		t.Fatalf("未知目标应明确失败: %+v", r2)
	}

	// 手动执行也必须走目标锁：并发 RunOnce 不得重叠。
	var inflight, peak int32
	var mu sync.Mutex
	exec2 := &fakeExecutor{fn: func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		return "", "", 0, nil
	}}
	s2 := NewScheduler(Deps{Store: store, Exec: exec2})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s2.RunOnce(context.Background(), "c1", "s1")
		}()
	}
	wg.Wait()
	mu.Lock()
	got := peak
	mu.Unlock()
	if got != 1 {
		t.Fatalf("同一目标的 RunOnce 不得重叠，峰值=%d", got)
	}
}

// TestSchedulerRunOnceRunsDisabledTarget 停用的检测项也必须能手动「立即执行」。
//
// 停用只应停掉定时探测：它不在 ListEnabledTargets 里（定时循环据此跳过），但用户点
// 「立即执行」时 RunOnce 要按 (检测项, 服务器) 精确查到目标、真正跑一次并落库。
// 修复前这条路径直接返回「未找到检测目标」，没有任何结果写入——页面于是显示空统计。
func TestSchedulerRunOnceRunsDisabledTarget(t *testing.T) {
	withFastInterval(t, time.Hour) // RunOnce 不依赖 ticker

	target := scriptTarget("c1", "s1", "echo disabled-once", nil)
	store := &fakeStore{}
	// 停用：只能被精确查找命中，启用列表里没有它。
	store.setLookupTargets(target)
	exec := &fakeExecutor{}
	s := NewScheduler(Deps{Store: store, Exec: exec})

	r := s.RunOnce(context.Background(), "c1", "s1")
	if !r.OK {
		t.Fatalf("停用检测项手动执行应真正执行: %+v", r)
	}
	if containsAll(r.Message, "未找到检测目标") {
		t.Fatalf("停用检测项不应报「未找到检测目标」: %+v", r)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("停用检测项手动执行应真正探测一次，实际 %d 次", len(exec.calls))
	}
	if store.resultCount() != 1 || store.stateCount() != 1 {
		t.Fatalf("停用检测项手动执行必须落库：results=%d states=%d", store.resultCount(), store.stateCount())
	}

	// 精查也查不到（例如该服务器已从目标列表移除）时仍要明确失败、零落库。
	other := s.RunOnce(context.Background(), "c1", "s2")
	if other.OK || !containsAll(other.Message, "未找到检测目标") {
		t.Fatalf("非目标的服务器应明确失败: %+v", other)
	}
	if store.resultCount() != 1 {
		t.Fatalf("失败路径不应落库: results=%d", store.resultCount())
	}
}

func TestSchedulerNilStoreAndDoubleStart(t *testing.T) {
	s := NewScheduler(Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()
	// 等第一个 Start 真正接管（否则本次调用会抢到所有权并因 ctx 不可取消而阻塞）
	if !waitFor(2*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.started
	}) {
		t.Fatalf("Start 未进入运行状态")
	}
	// 第二个 Start 应立即返回，不阻塞也不重复调度
	secondReturned := make(chan struct{})
	go func() {
		s.Start(context.Background())
		close(secondReturned)
	}()
	select {
	case <-secondReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("重复 Start 应立即返回")
	}
	r := s.RunOnce(context.Background(), "c1", "s1")
	if r.OK {
		t.Fatalf("无 Store 时 RunOnce 应失败: %+v", r)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Start 未退出")
	}
}

func TestSchedulerKeepsRunningAfterStoreWriteFailure(t *testing.T) {
	withFastInterval(t, 20*time.Millisecond)

	store := &fakeStore{saveErr: errors.New("disk full"), stateErr: errors.New("disk full")}
	store.setTargets(scriptTarget("c1", "s1", "echo a", nil))
	exec := &fakeExecutor{}
	s := NewScheduler(Deps{Store: store, Exec: exec})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()
	if !waitFor(2*time.Second, func() bool { return exec.callCount() >= 3 }) {
		t.Fatalf("落库失败不应让调度停摆，实际探测 %d 次", exec.callCount())
	}
	cancel()
	<-done
}

func TestIntervalOfClamp(t *testing.T) {
	withFastInterval(t, 10*time.Second)
	if got := intervalOf(Spec{IntervalS: 1}); got != 10*time.Second {
		t.Fatalf("间隔应被抬到下限 10s，得到 %s", got)
	}
	if got := intervalOf(Spec{IntervalS: 0}); got != 10*time.Second {
		t.Fatalf("未设置间隔应取下限 10s，得到 %s", got)
	}
	if got := intervalOf(Spec{IntervalS: 30}); got != 30*time.Second {
		t.Fatalf("合法间隔不应被改写，得到 %s", got)
	}
}

func TestMinIntervalDefaultIs10Seconds(t *testing.T) {
	// 契约要求最小 10s：生产默认值必须保持。
	saved := minInterval
	minInterval = time.Duration(MinIntervalS) * time.Second
	defer func() { minInterval = saved }()
	if minInterval != 10*time.Second {
		t.Fatalf("MinIntervalS=%d 应为 10", MinIntervalS)
	}
	if got := intervalOf(Spec{IntervalS: 5}); got != 10*time.Second {
		t.Fatalf("5s 应被抬到 10s，得到 %s", got)
	}
}

func TestSchedulerStringAndConcurrencyDefault(t *testing.T) {
	s := NewScheduler(Deps{})
	if cap(s.sem) != DefaultConcurrency {
		t.Fatalf("默认并发=%d want %d", cap(s.sem), DefaultConcurrency)
	}
	if s.String() == "" {
		t.Fatalf("String() 不应为空")
	}
	s2 := NewScheduler(Deps{Concurrency: -1})
	if cap(s2.sem) != DefaultConcurrency {
		t.Fatalf("非法并发应回落默认值")
	}
}
