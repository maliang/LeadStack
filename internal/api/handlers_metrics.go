package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/maliang/leadstack/internal/store"
)

// 指标历史接口的取值范围与上限。
const (
	// defaultMetricsRangeMS 是未指定 from/to 时的默认查询窗口（1 小时）。
	defaultMetricsRangeMS = int64(3600 * 1000)
	// maxRawRangeMS 是 raw 的最大回看窗口（与原始指标保留期一致）。
	maxRawRangeMS = int64(7*24*3600) * 1000
	// maxMinuteRangeMS 是 1m 的最大回看窗口。
	maxMinuteRangeMS = int64(30*24*3600) * 1000
	// maxHourRangeMS 是 1h 的最大回看窗口。
	maxHourRangeMS = int64(365*24*3600) * 1000
	// maxMetricPoints 限制单次返回的总采样点数，避免响应体过大。
	maxMetricPoints = 20000
	// maxRawNoDegradeMS 是 raw 不降级的最大跨度（6 小时）。
	//
	// 契约第 15 节：step=raw 且跨度 > 6h 时自动按 1m 返回，并在 step 字段回传实际值。
	maxRawNoDegradeMS = int64(6 * 3600 * 1000)
	// toClockSkewToleranceMS 是客户端 to 与服务端当前时间的容许偏差。
	//
	// 客户端常用「截断到秒的当前时间」作为 to（例如验收脚本的 date +%s * 1000），
	// 这会让刚刚写入的采样落在窗口之外；偏差在此容差内时按「到此刻为止」处理。
	toClockSkewToleranceMS = int64(5000)
)

// metricPoint 是时间序列上的一个点。
type metricPoint struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

// metricSeries 是单个指标的时间序列。
type metricSeries struct {
	Metric string        `json:"metric"`
	Points []metricPoint `json:"points"`
}

// metricsResponse 是指标历史响应体。
type metricsResponse struct {
	Series []metricSeries `json:"series"`
	Step   string         `json:"step"`
}

// handleServerMetrics 处理 GET /api/v1/servers/{id}/metrics?from=&to=&step=raw|1m|1h。
func (a *API) handleServerMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}

	step := strings.TrimSpace(r.URL.Query().Get("step"))
	if step == "" {
		step = store.RollupBucketRaw
	}
	if step != store.RollupBucketRaw && step != store.RollupBucketMinute && step != store.RollupBucketHour {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "step 只能是 raw|1m|1h")
		return
	}

	nowMS := a.nowMS()
	to, err := parseQueryInt(r, "to", nowMS)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "to 必须是 Unix 毫秒整数")
		return
	}
	// 容忍客户端时钟偏差（含「截断到秒」的 to）：与当前时间足够接近就按服务端时刻取整。
	if to > nowMS {
		to = nowMS
	} else if nowMS-to <= toClockSkewToleranceMS {
		to = nowMS
	}
	from, err := parseQueryInt(r, "from", to-defaultMetricsRangeMS)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "from 必须是 Unix 毫秒整数")
		return
	}

	// 大范围 raw 查询自动降级为 1m，避免一次返回几十万点。
	if step == store.RollupBucketRaw && to-from > maxRawNoDegradeMS {
		step = store.RollupBucketMinute
	}

	// 把窗口夹到该 step 的保留期内，避免无意义的超大查询。
	maxRange := maxRawRangeMS
	switch step {
	case store.RollupBucketMinute:
		maxRange = maxMinuteRangeMS
	case store.RollupBucketHour:
		maxRange = maxHourRangeMS
	}
	if to-from > maxRange {
		from = to - maxRange
	}
	if from >= to {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "from 必须小于 to")
		return
	}

	ctx := r.Context()
	if _, err := a.st.GetServer(ctx, id); err != nil {
		a.writeStoreError(w, r, err)
		return
	}

	resp := metricsResponse{Series: []metricSeries{}, Step: step}
	switch step {
	case store.RollupBucketRaw:
		samples, err := a.st.QuerySamplesRange(ctx, id, from, to, maxMetricPoints)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		resp.Series = groupSamples(samples)
	default:
		points, err := a.st.QueryRollupRange(ctx, id, step, from, to, maxMetricPoints)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		resp.Series = groupRollups(points)
	}
	writeData(w, http.StatusOK, resp)
}

// parseQueryInt 解析整数查询参数；缺省或空串返回 fallback。
func parseQueryInt(r *http.Request, key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

// groupSamples 把原始采样按指标聚合为序列（输入已按 metric, ts 升序）。
func groupSamples(samples []store.MetricSample) []metricSeries {
	index := make(map[string]int, 8)
	out := make([]metricSeries, 0, 8)
	for _, smp := range samples {
		i, ok := index[smp.Metric]
		if !ok {
			i = len(out)
			index[smp.Metric] = i
			out = append(out, metricSeries{Metric: smp.Metric, Points: make([]metricPoint, 0, 64)})
		}
		out[i].Points = append(out[i].Points, metricPoint{TS: smp.TS, Value: smp.Value})
	}
	return out
}

// groupRollups 把降采样汇总按指标聚合为序列（value 取 avg）。
func groupRollups(points []store.RollupPoint) []metricSeries {
	index := make(map[string]int, 8)
	out := make([]metricSeries, 0, 8)
	for _, p := range points {
		i, ok := index[p.Metric]
		if !ok {
			i = len(out)
			index[p.Metric] = i
			out = append(out, metricSeries{Metric: p.Metric, Points: make([]metricPoint, 0, 64)})
		}
		out[i].Points = append(out[i].Points, metricPoint{TS: p.TS, Value: p.Avg})
	}
	return out
}
