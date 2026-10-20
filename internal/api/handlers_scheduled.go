package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/maliang/leadstack/internal/cron"
	"github.com/maliang/leadstack/internal/store"
)

// 定时任务参数（契约第 42 节）。
const (
	defaultTaskTimeoutS    = 60
	maxTaskTimeoutS        = 3600
	defaultTaskConcurrency = 4
	maxTaskConcurrency     = 16
	maxTaskOutputBytes     = 256 << 10
	scheduledRunsDefault   = 100
)

// createScheduledTaskRequest 是创建/更新定时任务的请求体。
type createScheduledTaskRequest struct {
	Name        string   `json:"name"`
	Cron        string   `json:"cron"`
	Command     string   `json:"command"`
	TimeoutS    *int     `json:"timeout_s"`
	Enabled     *bool    `json:"enabled"`
	Concurrency *int     `json:"concurrency"`
	ServerIDs   []string `json:"server_ids"`
}

// validateCron 校验 cron 表达式并返回描述。
func validateCron(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", errors.New("cron 不能为空")
	}
	if err := cron.Validate(trimmed); err != nil {
		return "", fmt.Errorf("cron 表达式非法: %w", err)
	}
	return cron.Describe(trimmed), nil
}

// handleListScheduledTasks 处理 GET /api/v1/scheduled-tasks。
func (a *API) handleListScheduledTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := a.st.ListScheduledTasks(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if tasks == nil {
		tasks = []store.ScheduledTask{}
	}
	writeData(w, http.StatusOK, tasks)
}

// handleGetScheduledTask 处理 GET /api/v1/scheduled-tasks/{id}。
func (a *API) handleGetScheduledTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.loadScheduledTask(w, r)
	if err != nil {
		return
	}
	writeData(w, http.StatusOK, task)
}

// handleCreateScheduledTask 处理 POST /api/v1/scheduled-tasks。
func (a *API) handleCreateScheduledTask(w http.ResponseWriter, r *http.Request) {
	var req createScheduledTaskRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateText("name", name, 128, true); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if _, err := validateCron(req.Cron); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "command 不能为空")
		return
	}
	timeoutS := defaultTaskTimeoutS
	if req.TimeoutS != nil {
		timeoutS = *req.TimeoutS
	}
	if timeoutS < 1 || timeoutS > maxTaskTimeoutS {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("timeout_s 必须在 1-%d 之间", maxTaskTimeoutS))
		return
	}
	concurrency := defaultTaskConcurrency
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	if concurrency < 1 || concurrency > maxTaskConcurrency {
		writeError(w, http.StatusBadRequest, codeInvalidArgument,
			fmt.Sprintf("concurrency 必须在 1-%d 之间", maxTaskConcurrency))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ctx := r.Context()
	serverIDs := normalizeIDList(req.ServerIDs)
	if len(serverIDs) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "server_ids 不能为空")
		return
	}
	for _, id := range serverIDs {
		if _, err := a.st.GetServer(ctx, id); err != nil {
			a.writeStoreError(w, r, err)
			return
		}
	}
	nowMS := a.nowMS()
	task := &store.ScheduledTask{
		ID: uuid.NewString(), Name: name, Cron: strings.TrimSpace(req.Cron), Command: command,
		TimeoutS: timeoutS, Enabled: enabled, Concurrency: concurrency, CreatedAt: nowMS,
		ServerIDs: serverIDs,
	}
	if next, err := nextRunAt(task.Cron, nowMS); err == nil {
		task.NextRunAt = next
	}
	if err := a.st.CreateScheduledTask(ctx, task); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("创建定时任务", "task_id", task.ID, "name", task.Name, "cron", task.Cron, "targets", len(serverIDs))
	writeData(w, http.StatusCreated, task)
}

// handlePatchScheduledTask 处理 PATCH /api/v1/scheduled-tasks/{id}。
func (a *API) handlePatchScheduledTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.loadScheduledTask(w, r)
	if err != nil {
		return
	}
	var req createScheduledTaskRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	upd := store.ScheduledTaskUpdate{}
	if strings.TrimSpace(req.Name) != "" {
		name := strings.TrimSpace(req.Name)
		if err := validateText("name", name, 128, true); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, err.Error())
			return
		}
		upd.Name = &name
	}
	if strings.TrimSpace(req.Cron) != "" {
		if _, cerr := validateCron(req.Cron); cerr != nil {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, cerr.Error())
			return
		}
		c := strings.TrimSpace(req.Cron)
		upd.Cron = &c
		if next, nerr := nextRunAt(c, a.nowMS()); nerr == nil {
			upd.NextRunAt = next
		}
	}
	if strings.TrimSpace(req.Command) != "" {
		cmd := strings.TrimSpace(req.Command)
		upd.Command = &cmd
	}
	if req.TimeoutS != nil {
		if *req.TimeoutS < 1 || *req.TimeoutS > maxTaskTimeoutS {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("timeout_s 必须在 1-%d 之间", maxTaskTimeoutS))
			return
		}
		upd.TimeoutS = req.TimeoutS
	}
	if req.Concurrency != nil {
		if *req.Concurrency < 1 || *req.Concurrency > maxTaskConcurrency {
			writeError(w, http.StatusBadRequest, codeInvalidArgument,
				fmt.Sprintf("concurrency 必须在 1-%d 之间", maxTaskConcurrency))
			return
		}
		upd.Concurrency = req.Concurrency
	}
	if req.Enabled != nil {
		upd.Enabled = req.Enabled
	}
	if req.ServerIDs != nil {
		ids := normalizeIDList(req.ServerIDs)
		for _, id := range ids {
			if _, gerr := a.st.GetServer(r.Context(), id); gerr != nil {
				a.writeStoreError(w, r, gerr)
				return
			}
		}
		upd.ServerIDs = &ids
	}
	updated, err := a.st.UpdateScheduledTask(r.Context(), task.ID, upd)
	if err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("更新定时任务", "task_id", updated.ID, "enabled", updated.Enabled)
	writeData(w, http.StatusOK, updated)
}

// handleDeleteScheduledTask 处理 DELETE /api/v1/scheduled-tasks/{id}。
func (a *API) handleDeleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.loadScheduledTask(w, r)
	if err != nil {
		return
	}
	if err := a.st.DeleteScheduledTask(r.Context(), task.ID); err != nil {
		a.writeStoreError(w, r, err)
		return
	}
	a.logger.Info("删除定时任务", "task_id", task.ID)
	writeData(w, http.StatusOK, map[string]any{"id": task.ID})
}

// handleRunScheduledTask 处理 POST /api/v1/scheduled-tasks/{id}/run（立即执行一次）。
func (a *API) handleRunScheduledTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.loadScheduledTask(w, r)
	if err != nil {
		return
	}
	results := a.executeScheduledTask(r.Context(), *task)
	writeData(w, http.StatusOK, map[string]any{"task_id": task.ID, "runs": results})
}

// handleListScheduledTaskRuns 处理 GET /api/v1/scheduled-tasks/{id}/runs。
func (a *API) handleListScheduledTaskRuns(w http.ResponseWriter, r *http.Request) {
	task, err := a.loadScheduledTask(w, r)
	if err != nil {
		return
	}
	params, perr := parsePage(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, perr.Error())
		return
	}
	limit := scheduledRunsDefault
	if params.Paged {
		limit = params.fetchLimit(scheduledRunsDefault)
	}
	runs, err := a.st.ListScheduledTaskRuns(r.Context(), task.ID, 0, limit)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if runs == nil {
		runs = []store.ScheduledTaskRun{}
	}
	start, end, meta := params.window(len(runs))
	if !params.Paged {
		start, end = 0, len(runs)
	}
	writePagedList(w, params, runs[start:end], meta)
}

// loadScheduledTask 读取路径上的任务。
func (a *API) loadScheduledTask(w http.ResponseWriter, r *http.Request) (*store.ScheduledTask, error) {
	id := chi.URLParam(r, "taskID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少任务 ID")
		return nil, errors.New("缺少任务 ID")
	}
	task, err := a.st.GetScheduledTask(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, r, err)
		return nil, err
	}
	return task, nil
}

// nextRunAt 计算下一次触发时间（Unix 毫秒）。
func nextRunAt(expr string, fromMS int64) (*int64, error) {
	schedule, err := cron.Parse(strings.TrimSpace(expr))
	if err != nil {
		return nil, err
	}
	next, err := schedule.Next(time.UnixMilli(fromMS))
	if err != nil {
		return nil, err
	}
	ms := next.UnixMilli()
	return &ms, nil
}

// executeScheduledTask 在全部目标上并发执行一次任务，并把结果落库。
func (a *API) executeScheduledTask(ctx context.Context, task store.ScheduledTask) []store.ScheduledTaskRun {
	results := make([]store.ScheduledTaskRun, 0, len(task.ServerIDs))
	if a.remoteExec == nil {
		a.logger.Warn("远程执行通道未启用，定时任务未执行", "task_id", task.ID)
		return results
	}
	concurrency := task.Concurrency
	if concurrency <= 0 {
		concurrency = defaultTaskConcurrency
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, serverID := range task.ServerIDs {
		wg.Add(1)
		go func(serverID string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			started := time.Now()
			stdout, stderr, exitCode, err := a.remoteExec.Exec(ctx, serverID, task.Command, task.TimeoutS)
			duration := time.Since(started).Milliseconds()
			run := store.ScheduledTaskRun{
				TaskID: task.ID, ServerID: serverID, TS: started.UnixMilli(),
				DurationMS: &duration,
			}
			status := "success"
			switch {
			case err != nil:
				status = "failed"
				msg := err.Error()
				run.Message = &msg
			case exitCode != 0:
				status = "failed"
				msg := fmt.Sprintf("退出码 %d", exitCode)
				run.Message = &msg
			}
			run.Status = status
			code := exitCode
			run.ExitCode = &code
			run.Stdout = truncateString(stdout, maxTaskOutputBytes)
			run.Stderr = truncateString(stderr, maxTaskOutputBytes)
			if err := a.st.InsertScheduledTaskRun(context.WithoutCancel(ctx), &run); err != nil {
				a.logger.Warn("写入定时任务执行记录失败", "task_id", task.ID, "err", err)
			}
			mu.Lock()
			results = append(results, run)
			mu.Unlock()
		}(serverID)
	}
	wg.Wait()

	nowMS := a.nowMS()
	upd := store.ScheduledTaskUpdate{LastRunAt: &nowMS}
	if next, err := nextRunAt(task.Cron, nowMS); err == nil {
		upd.NextRunAt = next
	}
	if _, err := a.st.UpdateScheduledTask(context.WithoutCancel(ctx), task.ID, upd); err != nil {
		a.logger.Warn("更新定时任务运行时间失败", "task_id", task.ID, "err", err)
	}
	return results
}

// truncateString 截断字符串到 maxBytes 字节（返回指针；空串返回 nil）。
func truncateString(s string, maxBytes int) *string {
	if s == "" {
		return nil
	}
	if len(s) > maxBytes {
		s = s[:maxBytes] + "\n…（输出已截断）"
	}
	return &s
}

// RunScheduledTaskEngine 周期检查并按 cron 触发定时任务，直到 ctx 取消。
//
// 每个任务记录「上次触发分钟」避免 tick 很小时同一分钟重复触发。
func (a *API) RunScheduledTaskEngine(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = time.Minute
	}
	a.logger.Info("定时任务引擎启动", "tick", tick.String())
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	lastFired := map[string]int64{}
	for {
		select {
		case <-ctx.Done():
			a.logger.Info("定时任务引擎退出")
			return
		case <-ticker.C:
			tasks, err := a.st.ListEnabledScheduledTasks(ctx)
			if err != nil {
				a.logger.Warn("定时任务引擎读取任务失败", "err", err)
				continue
			}
			now := time.Now()
			nowMS := now.UnixMilli()
			for _, task := range tasks {
				schedule, perr := cron.Parse(strings.TrimSpace(task.Cron))
				if perr != nil {
					continue
				}
				// 到期判定：把「当前分钟的起点」当作基准，看调度是否在该分钟内触发。
				// 这样即使 tick 很细（1s），也能在分钟起点之后立刻发现并触发。
				minuteStart := now.Truncate(time.Minute)
				next, nerr := schedule.Next(minuteStart.Add(-time.Second))
				if nerr != nil || next.After(minuteStart) {
					continue
				}
				// 同一分钟内只触发一次。
				prev := lastFired[task.ID]
				if prev/60000 == minuteStart.Unix()/60 {
					continue
				}
				lastFired[task.ID] = nowMS
				a.logger.Info("触发定时任务", "task_id", task.ID, "name", task.Name)
				go a.executeScheduledTask(ctx, task)
			}
		}
	}
}
