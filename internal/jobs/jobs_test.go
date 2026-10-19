package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maliang/leadstack/internal/checker"
	"github.com/maliang/leadstack/pkg/protocol"
)

// 编译期断言：Dispatcher 必须满足 checker.Executor（契约 14.2）。
// 放在测试文件里，避免生产代码对 checker 产生依赖方向。
var _ checker.Executor = (*Dispatcher)(nil)

// sentJob 记录一次 SendJob 的内容，便于断言下发的信封。
type sentJob struct {
	serverID string
	env      protocol.Envelope
	job      protocol.JobExec
}

// fakeSender 是测试用的假 Hub：可配置在线状态、发送错误，并用 onSend 模拟 agent 回帧。
type fakeSender struct {
	mu      sync.Mutex
	online  map[string]bool
	sent    []sentJob
	sendErr error
	onSend  func(serverID string, job protocol.JobExec)
}

func newFakeSender(servers ...string) *fakeSender {
	f := &fakeSender{online: make(map[string]bool, len(servers))}
	for _, s := range servers {
		f.online[s] = true
	}
	return f
}

func (f *fakeSender) IsOnline(serverID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[serverID]
}

func (f *fakeSender) setOnline(serverID string, online bool) {
	f.mu.Lock()
	f.online[serverID] = online
	f.mu.Unlock()
}

func (f *fakeSender) setSendErr(err error) {
	f.mu.Lock()
	f.sendErr = err
	f.mu.Unlock()
}

func (f *fakeSender) SendJob(serverID string, envelope []byte) error {
	f.mu.Lock()
	sendErr := f.sendErr
	onSend := f.onSend
	f.mu.Unlock()
	if sendErr != nil {
		return sendErr
	}

	var env protocol.Envelope
	if err := json.Unmarshal(envelope, &env); err != nil {
		return fmt.Errorf("envelope 不是合法 JSON: %w", err)
	}
	if env.Type != protocol.TypeJobExec {
		return fmt.Errorf("envelope type = %q，期望 %q", env.Type, protocol.TypeJobExec)
	}
	var job protocol.JobExec
	if err := json.Unmarshal(env.Payload, &job); err != nil {
		return fmt.Errorf("payload 不是合法 JobExec: %w", err)
	}

	f.mu.Lock()
	f.sent = append(f.sent, sentJob{serverID: serverID, env: env, job: job})
	f.mu.Unlock()

	if onSend != nil {
		onSend(serverID, job)
	}
	return nil
}

func (f *fakeSender) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) firstJob() (sentJob, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentJob{}, false
	}
	return f.sent[0], true
}

// setupDispatcher 创建 Dispatcher 并把 onSend 回调（可拿到 d）装上。
func setupDispatcher(f *fakeSender, opts Options, onSend func(d *Dispatcher, serverID string, job protocol.JobExec)) *Dispatcher {
	d := NewDispatcher(f, opts)
	if onSend != nil {
		f.mu.Lock()
		f.onSend = func(serverID string, job protocol.JobExec) { onSend(d, serverID, job) }
		f.mu.Unlock()
	}
	return d
}

// waitForPending 轮询等待等待表长度达到期望值。
func waitForPending(t *testing.T, d *Dispatcher, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.pendingCount() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待 pending=%d 超时，当前 %d", want, d.pendingCount())
}

// TestExecRoundTrip 验证正常往返：ack / 乱序日志 / 重复 seq / 重复 done / 未知 stream。
func TestExecRoundTrip(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleAck(serverID, job.RunID)
		d.HandleLog(serverID, job.RunID, 2, "stdout", "C")
		d.HandleLog(serverID, job.RunID, 0, "stdout", "A")
		d.HandleLog(serverID, job.RunID, 1, "stdout", "B")
		d.HandleLog(serverID, job.RunID, 1, "stdout", "B-dup") // 重复 seq：只保留第一份
		d.HandleLog(serverID, job.RunID, 3, "stderr", "E")
		d.HandleLog(serverID, job.RunID, 4, "stdout", "")  // 空块忽略
		d.HandleLog(serverID, job.RunID, 5, "weird", "忽略") // 未知 stream 忽略
		d.HandleDone(serverID, job.RunID, 0, "")
		d.HandleDone(serverID, job.RunID, 0, "") // 重复 done 忽略
	})

	stdout, stderr, code, err := d.Exec(context.Background(), "srv-1", "echo hi", 5)
	if err != nil {
		t.Fatalf("Exec 返回错误: %v", err)
	}
	if stdout != "ABC" {
		t.Errorf("stdout = %q，期望按 seq 排序去重后的 %q", stdout, "ABC")
	}
	if stderr != "E" {
		t.Errorf("stderr = %q，期望 %q", stderr, "E")
	}
	if code != 0 {
		t.Errorf("exitCode = %d，期望 0", code)
	}
	if got := d.pendingCount(); got != 0 {
		t.Errorf("完成后等待表未清空：%d", got)
	}

	job, ok := f.firstJob()
	if !ok {
		t.Fatal("未下发任何任务")
	}
	if job.serverID != "srv-1" {
		t.Errorf("下发目标 = %q，期望 srv-1", job.serverID)
	}
	if job.job.Kind != "command" || job.job.Command != "echo hi" || job.job.TimeoutS != 5 {
		t.Errorf("JobExec 内容错误: %+v", job.job)
	}
	if job.job.RunID == "" || job.env.ID != job.job.RunID {
		t.Errorf("runID / envelope.ID 不一致: run_id=%q env_id=%q", job.job.RunID, job.env.ID)
	}
}

// TestExecNonZeroExit 验证命令非 0 退出：exitCode 透传，err 为 nil。
func TestExecNonZeroExit(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleLog(serverID, job.RunID, 0, "stdout", "boom")
		d.HandleDone(serverID, job.RunID, 7, "")
	})
	stdout, _, code, err := d.Exec(context.Background(), "srv-1", "exit 7", 5)
	if err != nil {
		t.Fatalf("非 0 退出不应是 error: %v", err)
	}
	if code != 7 {
		t.Errorf("exitCode = %d，期望 7", code)
	}
	if stdout != "boom" {
		t.Errorf("stdout = %q，期望 boom", stdout)
	}
}

// TestExecAgentError 验证 agent 回报执行失败（job.done.error 非空）时返回 error。
func TestExecAgentError(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleLog(serverID, job.RunID, 0, "stdout", "partial")
		d.HandleDone(serverID, job.RunID, -1, "启动命令失败: exec: not found")
	})
	stdout, _, code, err := d.Exec(context.Background(), "srv-1", "nope", 5)
	if err == nil {
		t.Fatal("agent 回报失败时 Exec 应返回 error")
	}
	if !strings.Contains(err.Error(), "启动命令失败") {
		t.Errorf("error 未包含 agent 的原因: %v", err)
	}
	if stdout != "partial" {
		t.Errorf("应返回已收集的部分输出，实际 %q", stdout)
	}
	if code != -1 {
		t.Errorf("exitCode = %d，期望 -1", code)
	}
	if d.pendingCount() != 0 {
		t.Errorf("等待表未清空：%d", d.pendingCount())
	}
}

// TestExecOfflineFastFail 验证离线时立即失败且不下发。
func TestExecOfflineFastFail(t *testing.T) {
	f := newFakeSender("srv-2") // srv-1 离线
	d := setupDispatcher(f, Options{}, nil)
	_, _, _, err := d.Exec(context.Background(), "srv-1", "echo hi", 5)
	if !errors.Is(err, ErrServerOffline) {
		t.Fatalf("err = %v，期望 ErrServerOffline", err)
	}
	if f.sentCount() != 0 {
		t.Errorf("离线时不应下发任务，实际下发 %d 次", f.sentCount())
	}
	if d.pendingCount() != 0 {
		t.Errorf("离线失败后等待表应为空：%d", d.pendingCount())
	}
}

// TestExecNilSender 验证未注入 Sender 时安全失败（不 panic）。
func TestExecNilSender(t *testing.T) {
	d := NewDispatcher(nil, Options{})
	_, _, _, err := d.Exec(context.Background(), "srv-1", "echo hi", 5)
	if !errors.Is(err, ErrServerOffline) {
		t.Fatalf("err = %v，期望 ErrServerOffline", err)
	}
}

// TestExecSendErrorCleansUp 验证 SendJob 失败时立即返回并清理等待者。
func TestExecSendErrorCleansUp(t *testing.T) {
	f := newFakeSender("srv-1")
	f.setSendErr(errors.New("hub 已关闭"))
	d := setupDispatcher(f, Options{}, nil)
	_, _, _, err := d.Exec(context.Background(), "srv-1", "echo hi", 5)
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("err = %v，期望 ErrSendFailed", err)
	}
	if d.pendingCount() != 0 {
		t.Errorf("发送失败后等待表应为空：%d", d.pendingCount())
	}
}

// TestExecContextCancelCleansUp 验证 ctx 取消会清理等待者。
func TestExecContextCancelCleansUp(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, nil) // onSend 不回帧
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, _, _, err := d.Exec(ctx, "srv-1", "sleep 100", 30)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v，期望 context.DeadlineExceeded", err)
	}
	if d.pendingCount() != 0 {
		t.Errorf("ctx 取消后等待表应为空：%d", d.pendingCount())
	}
}

// TestExecTimeoutCleansUpRepeatedly 连续 200 次超时，验证等待表与 goroutine 都不泄漏。
func TestExecTimeoutCleansUpRepeatedly(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{Timeout: 5 * time.Millisecond}, nil)

	runtime.GC()
	before := runtime.NumGoroutine()

	const rounds = 200
	for i := 0; i < rounds; i++ {
		_, _, _, err := d.Exec(context.Background(), "srv-1", "sleep 100", 0)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("第 %d 次 err = %v，期望 ErrTimeout", i, err)
		}
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()

	if got := d.pendingCount(); got != 0 {
		t.Errorf("200 次超时后等待表残留 %d 条", got)
	}
	if f.sentCount() != rounds {
		t.Errorf("下发次数 = %d，期望 %d", f.sentCount(), rounds)
	}
	if after > before+5 {
		t.Errorf("goroutine 疑似泄漏：before=%d after=%d", before, after)
	}
}

// TestExecTimeoutSApplies 验证 timeoutS 覆盖默认 Options.Timeout。
func TestExecTimeoutSApplies(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{Timeout: 10 * time.Second}, nil)
	start := time.Now()
	_, _, _, err := d.Exec(context.Background(), "srv-1", "sleep 100", 1)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v，期望 ErrTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("timeoutS=1 应在 ~1s 内超时，实际 %v", elapsed)
	}
	if d.pendingCount() != 0 {
		t.Errorf("超时后等待表应为空：%d", d.pendingCount())
	}
}

// TestConcurrentSameServerIsolation 验证同一台机器并发的任务各自拿到自己的结果。
func TestConcurrentSameServerIsolation(t *testing.T) {
	const n = 8
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{MaxConcurrentPerServer: 4}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleAck(serverID, job.RunID)
		d.HandleLog(serverID, job.RunID, 1, "stdout", "-"+job.Command)
		d.HandleLog(serverID, job.RunID, 0, "stdout", "out")
		d.HandleDone(serverID, job.RunID, 0, "")
	})

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := "out-" + strconv.Itoa(i)
			stdout, stderr, code, err := d.Exec(context.Background(), "srv-1", strconv.Itoa(i), 5)
			if err != nil {
				errs <- fmt.Errorf("任务 %d: %w", i, err)
				return
			}
			if stdout != want || stderr != "" || code != 0 {
				errs <- fmt.Errorf("任务 %d 结果串了: stdout=%q stderr=%q code=%d，期望 %q", i, stdout, stderr, code, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if d.pendingCount() != 0 {
		t.Errorf("并发完成后等待表应为空：%d", d.pendingCount())
	}
}

// TestPerServerQueueLimit 验证单机并发上限：超出的任务排队，不丢结果。
func TestPerServerQueueLimit(t *testing.T) {
	f := newFakeSender("srv-1")
	gate := make(chan struct{})
	d := setupDispatcher(f, Options{MaxConcurrentPerServer: 1}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		if job.Command == "block" {
			<-gate
			d.HandleDone(serverID, job.RunID, 0, "")
			return
		}
		d.HandleDone(serverID, job.RunID, 0, "")
	})

	first := make(chan error, 1)
	go func() {
		stdout, _, code, err := d.Exec(context.Background(), "srv-1", "block", 5)
		if err == nil && (stdout != "" || code != 0) {
			err = fmt.Errorf("第一个任务结果异常: %q/%d", stdout, code)
		}
		first <- err
	}()
	waitForPending(t, d, 1)

	second := make(chan string, 1)
	go func() {
		stdout, _, _, err := d.Exec(context.Background(), "srv-1", "quick", 5)
		if err != nil {
			stdout = "ERR:" + err.Error()
		}
		second <- stdout
	}()

	// 第一个任务占着唯一槽位，第二个必须排队（尚未下发）。
	// 注意：排队中的任务拿到槽位后才登记等待表，所以此时 pending 仍为 1。
	time.Sleep(100 * time.Millisecond)
	if got := f.sentCount(); got != 1 {
		t.Errorf("并发上限为 1 时不应同时下发，sent=%d", got)
	}
	if got := d.pendingCount(); got != 1 {
		t.Errorf("排队中的任务不应登记等待表，pending=%d，期望 1", got)
	}

	close(gate)
	if err := <-first; err != nil {
		t.Errorf("第一个任务失败: %v", err)
	}
	if got := <-second; got != "" {
		t.Errorf("第二个任务 stdout = %q，期望空", got)
	}
	if f.sentCount() != 2 {
		t.Errorf("sent=%d，期望 2", f.sentCount())
	}
	if d.pendingCount() != 0 {
		t.Errorf("等待表未清空：%d", d.pendingCount())
	}
}

// TestRunIDScopedByServer 验证 (serverID, runID) 双键隔离：
// 用错误 serverID 回帧不会交付，正确 serverID 才能唤醒。
func TestRunIDScopedByServer(t *testing.T) {
	f := newFakeSender("srv-1", "srv-2")
	var mu sync.Mutex
	var runID string
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		mu.Lock()
		runID = job.RunID
		mu.Unlock()
		// 故意用另一台服务器的 ID 回帧：必须被静默忽略。
		d.HandleDone("srv-2", job.RunID, 0, "错误服务器")
	})

	result := make(chan error, 1)
	go func() {
		_, _, _, err := d.Exec(context.Background(), "srv-1", "echo hi", 5)
		result <- err
	}()

	waitForPending(t, d, 1)
	time.Sleep(50 * time.Millisecond)
	if d.pendingCount() != 1 {
		t.Fatal("错误 serverID 的 job.done 不应交付结果")
	}
	mu.Lock()
	gotRunID := runID
	mu.Unlock()
	d.HandleDone("srv-1", gotRunID, 0, "")

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Exec 返回错误: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("正确 serverID 的 job.done 未唤醒 Exec")
	}
	if d.pendingCount() != 0 {
		t.Errorf("等待表未清空：%d", d.pendingCount())
	}
}

// TestFailServerWakesAllWaiters 验证连接断开时唤醒该服务器所有等待者，且不影响其它服务器。
func TestFailServerWakesAllWaiters(t *testing.T) {
	f := newFakeSender("srv-1", "srv-2")
	d := setupDispatcher(f, Options{MaxConcurrentPerServer: 8}, nil) // 不回帧

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, _, _, err := d.Exec(context.Background(), "srv-1", "sleep 100", 30)
			results <- err
		}()
	}
	go func() {
		_, _, _, err := d.Exec(ctx2, "srv-2", "sleep 100", 30)
		results <- err
	}()

	waitForPending(t, d, 4)

	cause := errors.New("websocket 连接断开")
	d.FailServer("srv-1", cause)

	for i := 0; i < 3; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrServerOffline) || !errors.Is(err, cause) {
				t.Errorf("被唤醒任务的 err = %v，期望同时包裹 ErrServerOffline 与 cause", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("FailServer 未唤醒全部等待者")
		}
	}
	if d.pendingCount() != 1 {
		t.Errorf("FailServer 不应影响其它服务器，pending=%d，期望 1", d.pendingCount())
	}

	// 清理 srv-2 的等待者。
	cancel2()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("srv-2 的 err = %v，期望 context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消 ctx 后 srv-2 的 Exec 未返回")
	}
	if d.pendingCount() != 0 {
		t.Errorf("等待表未清空：%d", d.pendingCount())
	}
}

// TestFailServerUnknownServerNoop 验证对未知 serverID 调用 FailServer 无副作用。
func TestFailServerUnknownServerNoop(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, nil)
	d.FailServer("nope", nil) // 不应 panic
	d.FailServer("nope", errors.New("x"))
	if d.pendingCount() != 0 {
		t.Errorf("等待表不应有变化：%d", d.pendingCount())
	}
}

// TestHandleUnknownRunIDIgnored 验证三个 Handle* 对未知 runID 静默忽略。
func TestHandleUnknownRunIDIgnored(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, nil)
	d.HandleAck("srv-1", "ghost-run")
	d.HandleLog("srv-1", "ghost-run", 0, "stdout", "x")
	d.HandleDone("srv-1", "ghost-run", 0, "")
	d.HandleLog("srv-unknown", "ghost-run", 0, "stdout", "x")
	if d.pendingCount() != 0 {
		t.Errorf("未知 runID 不应改变等待表：%d", d.pendingCount())
	}
}

// TestOutputTruncation 验证 stdout/stderr 各自截断到上限并追加标记。
func TestOutputTruncation(t *testing.T) {
	const limit = 64
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{MaxOutputBytes: limit}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleLog(serverID, job.RunID, 0, "stdout", strings.Repeat("A", 50))
		d.HandleLog(serverID, job.RunID, 1, "stdout", strings.Repeat("B", 50))
		d.HandleLog(serverID, job.RunID, 2, "stderr", strings.Repeat("C", 10))
		d.HandleDone(serverID, job.RunID, 0, "")
	})
	stdout, stderr, _, err := d.Exec(context.Background(), "srv-1", "big", 5)
	if err != nil {
		t.Fatalf("Exec 返回错误: %v", err)
	}
	if !strings.HasSuffix(stdout, truncatedMarker) {
		t.Errorf("stdout 未追加截断标记：%q", stdout)
	}
	if len(stdout) != limit+len(truncatedMarker) {
		t.Errorf("stdout 长度 = %d，期望 %d", len(stdout), limit+len(truncatedMarker))
	}
	if !strings.HasPrefix(stdout, strings.Repeat("A", 50)+strings.Repeat("B", 14)) {
		t.Errorf("stdout 前 %d 字节内容不符合预期：%q", limit, stdout)
	}
	if stderr != strings.Repeat("C", 10) {
		t.Errorf("stderr 不该被截断：%q", stderr)
	}
}

// TestTruncationKeepsValidUTF8 验证截断不会产生非法 UTF-8。
func TestTruncationKeepsValidUTF8(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{MaxOutputBytes: 5}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		// "中" 占 3 字节，5 字节只能放下一个完整的「中」。
		d.HandleLog(serverID, job.RunID, 0, "stdout", strings.Repeat("中", 3))
		d.HandleDone(serverID, job.RunID, 0, "")
	})
	stdout, _, _, err := d.Exec(context.Background(), "srv-1", "utf8", 5)
	if err != nil {
		t.Fatalf("Exec 返回错误: %v", err)
	}
	if !utf8.ValidString(stdout) {
		t.Errorf("截断结果不是合法 UTF-8：%q", stdout)
	}
	if stdout != "中"+truncatedMarker {
		t.Errorf("stdout = %q，期望 %q", stdout, "中"+truncatedMarker)
	}
}

// TestDefaultOptions 验证零值 Options 落入文档默认值。
func TestDefaultOptions(t *testing.T) {
	f := newFakeSender("srv-1")
	d := NewDispatcher(f, Options{})
	if d.opts.MaxConcurrentPerServer != DefaultMaxConcurrentPerServer {
		t.Errorf("MaxConcurrentPerServer = %d，期望 %d", d.opts.MaxConcurrentPerServer, DefaultMaxConcurrentPerServer)
	}
	if d.opts.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Errorf("MaxOutputBytes = %d，期望 %d", d.opts.MaxOutputBytes, DefaultMaxOutputBytes)
	}
	if d.opts.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v，期望 %v", d.opts.Timeout, DefaultTimeout)
	}
}

// TestAgentTimeoutFollowsContextDeadline 验证下发给 agent 的 timeout_s 会贴近 ctx 的更早 deadline。
func TestAgentTimeoutFollowsContextDeadline(t *testing.T) {
	f := newFakeSender("srv-1")
	d := setupDispatcher(f, Options{}, func(d *Dispatcher, serverID string, job protocol.JobExec) {
		d.HandleDone(serverID, job.RunID, 0, "")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, _, err := d.Exec(ctx, "srv-1", "echo hi", 60); err != nil {
		t.Fatalf("Exec 返回错误: %v", err)
	}
	job, ok := f.firstJob()
	if !ok {
		t.Fatal("未下发任务")
	}
	if job.job.TimeoutS < 1 || job.job.TimeoutS > 5 {
		t.Errorf("agent timeout_s = %d，期望落在 [1,5]（ctx 剩余时间约 5s）", job.job.TimeoutS)
	}
}
