package checker

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ---------- Executor 假实现 ----------

type execCall struct {
	serverID string
	command  string
	timeoutS int
}

// fakeExecutor 记录调用并按脚本返回结果，同时统计全局并发峰值。
type fakeExecutor struct {
	mu          sync.Mutex
	calls       []execCall
	fn          func(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error)
	inflight    int
	maxInflight int
}

func (f *fakeExecutor) Exec(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, execCall{serverID: serverID, command: command, timeoutS: timeoutS})
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	fn := f.fn
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()

	if fn != nil {
		return fn(ctx, serverID, command, timeoutS)
	}
	return "1\n", "", 0, nil
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeExecutor) lastCall() (execCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return execCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func (f *fakeExecutor) peakInflight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

// realExecutor 通过本机 `sh -c` 真跑命令——这正是 agent 侧 job.exec 的语义
// （internal/agent 收到 job.exec 后就是 sh -c），因此可以拿来验证生成的命令。
type realExecutor struct {
	mu    sync.Mutex
	calls []execCall
}

func (e *realExecutor) Exec(ctx context.Context, serverID, command string, timeoutS int) (string, string, int, error) {
	e.mu.Lock()
	e.calls = append(e.calls, execCall{serverID: serverID, command: command, timeoutS: timeoutS})
	e.mu.Unlock()

	runCtx := ctx
	if timeoutS > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	// 与 agent 侧 job.exec 同语义：命令独占进程组，超时杀整组，
	// 否则 sh 派生的子进程会一直攥着输出管道，Run 要等它自然退出（Linux/dash 上必现）。
	configureRealExec(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.String(), stderr.String(), ee.ExitCode(), nil // 非 0 退出不算传输层错误
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

func (e *realExecutor) lastCommand() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) == 0 {
		return ""
	}
	return e.calls[len(e.calls)-1].command
}

// ---------- Store 假实现 ----------

type storedResult struct {
	checkID  string
	serverID string
	result   Result
}

type storedState struct {
	checkID  string
	serverID string
	ok       bool
	ts       int64
}

type fakeStore struct {
	mu      sync.Mutex
	targets []Target
	// lookup 模拟「按 (检测项, 服务器) 精确查、忽略 enabled」：停用的检测项目标放在这里，
	// 不会出现在 targets（启用列表）里。两者分开才能复现「停用项点立即执行」的场景。
	lookup   map[targetKey]Target
	results  []storedResult
	states   []storedState
	listErr  error
	saveErr  error
	stateErr error
	getErr   error
}

func (s *fakeStore) ListEnabledTargets(ctx context.Context) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]Target, len(s.targets))
	copy(out, s.targets)
	return out, nil
}

// GetCheckTarget 精确查目标（忽略 enabled），命中 lookup 才有结果。
func (s *fakeStore) GetCheckTarget(ctx context.Context, checkID, serverID string) (Target, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return Target{}, false, s.getErr
	}
	t, ok := s.lookup[targetKey{checkID: checkID, serverID: serverID}]
	if !ok {
		return Target{}, false, nil
	}
	return t, true, nil
}

func (s *fakeStore) SaveResult(ctx context.Context, checkID, serverID string, r Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.results = append(s.results, storedResult{checkID: checkID, serverID: serverID, result: r})
	return nil
}

func (s *fakeStore) UpdateState(ctx context.Context, checkID, serverID string, ok bool, ts int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateErr != nil {
		return s.stateErr
	}
	s.states = append(s.states, storedState{checkID: checkID, serverID: serverID, ok: ok, ts: ts})
	return nil
}

func (s *fakeStore) setTargets(ts ...Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append([]Target(nil), ts...)
}

func (s *fakeStore) addTarget(t Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, t)
}

// setLookupTargets 设置「精确查找」可见的目标（可含停用项），不影响启用列表。
func (s *fakeStore) setLookupTargets(ts ...Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookup = make(map[targetKey]Target, len(ts))
	for _, t := range ts {
		s.lookup[targetKey{checkID: t.CheckID, serverID: t.ServerID}] = t
	}
}

func (s *fakeStore) resultCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

func (s *fakeStore) resultsForServer(serverID string) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Result
	for _, r := range s.results {
		if r.serverID == serverID {
			out = append(out, r.result)
		}
	}
	return out
}

func (s *fakeStore) stateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.states)
}

func (s *fakeStore) lastState() (storedState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.states) == 0 {
		return storedState{}, false
	}
	return s.states[len(s.states)-1], true
}

// ---------- 测试辅助 ----------

// waitFor 轮询条件直到成立或超时，返回是否成立。
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// withFastInterval 把调度间隔下限降到 d，让调度器单测不必等 10s。仅测试使用。
func withFastInterval(t interface{ Cleanup(func()) }, d time.Duration) {
	old := minInterval
	minInterval = d
	t.Cleanup(func() { minInterval = old })
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
