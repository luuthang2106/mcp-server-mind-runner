package egress

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Log usage gateway (logs/egress.log trên máy user — user gửi file này để
// phân tích chi phí/cache). KHÔNG BAO GIỜ ghi nội dung request/response:
// chỉ số đo (bytes, token, cache, độ trễ, lỗi).
//
//   - bất thường → luôn ghi (WARN): lỗi, request lớn, chậm.
//   - bình thường → lấy mẫu tối thiểu (INFO): lần gọi đầu mỗi op/model trong
//     process (mốc so cache), sau đó 1/sampleEvery.
//   - tổng hợp → 1 dòng mỗi giờ + khi process thoát (FlushUsage), đủ để tính
//     tổng token / tỉ lệ cache mà không cần log từng request.
var (
	sampleEvery         = 50
	anomalyPromptTokens = 30_000
	anomalyRequestBytes = 256 << 10 // dữ liệu media (omni) không tính ngưỡng này
	anomalyLatency      = 60 * time.Second
	usageFlushEvery     = time.Hour
	nowFn               = time.Now
)

// usage gom các biến thể field usage của OpenAI / DeepSeek / DashScope.
type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	InputTokens      int `json:"input_tokens"` // DashScope rerank
	CacheHit         int `json:"prompt_cache_hit_tokens"`
	Details          *struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u usage) prompt() int {
	if u.PromptTokens > 0 {
		return u.PromptTokens
	}
	if u.InputTokens > 0 {
		return u.InputTokens
	}
	return u.TotalTokens - u.CompletionTokens
}

// cached: -1 = gateway không trả field cache (khác với 0 = có trả nhưng không trúng).
func (u usage) cached(raw map[string]json.RawMessage) int {
	if u.Details != nil {
		return max(u.Details.Cached, u.CacheHit)
	}
	if _, ok := raw["prompt_cache_hit_tokens"]; ok {
		return u.CacheHit
	}
	return -1
}

// callInfo: số đo của một lần doJSON.
type callInfo struct {
	reqBytes  int
	respBytes int
	status    int
	latency   time.Duration
	usage     usage
	cached    int
	hasUsage  bool
}

func parseUsage(data []byte) (usage, int, bool) {
	var wrap struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(data, &wrap) != nil || len(wrap.Usage) == 0 || string(wrap.Usage) == "null" {
		return usage{}, -1, false
	}
	var u usage
	var raw map[string]json.RawMessage
	if json.Unmarshal(wrap.Usage, &u) != nil || json.Unmarshal(wrap.Usage, &raw) != nil {
		return usage{}, -1, false
	}
	return u, u.cached(raw), true
}

// purposeKey: nhãn mục đích (vd loại job "extract_session", "recall") gắn vào ctx.
type purposeKey struct{}

// WithPurpose gắn nhãn mục đích cho mọi lần gọi egress trong ctx (chỉ để log).
func WithPurpose(ctx context.Context, p string) context.Context {
	return context.WithValue(ctx, purposeKey{}, p)
}

func purposeOf(ctx context.Context) string {
	if p, ok := ctx.Value(purposeKey{}).(string); ok && p != "" {
		return p
	}
	return "-"
}

type aggKey struct{ op, model, purpose string }

type agg struct {
	calls, errors, slow, big      int
	reqBytes                      int64
	prompt, completion, cachedSum int64
	cacheReported                 int // số call gateway có trả field cache
	maxPrompt                     int
	maxLatency, totalLatency      time.Duration
}

type meter struct {
	mu        sync.Mutex
	log       *slog.Logger
	seen      map[aggKey]int
	aggs      map[aggKey]*agg
	lastFlush time.Time
}

func newMeter() *meter {
	return &meter{seen: map[aggKey]int{}, aggs: map[aggKey]*agg{}, lastFlush: nowFn()}
}

// SetUsageLog bật log usage vào lg (nil → tắt). Gọi trước khi dùng Egress.
func (e *Egress) SetUsageLog(lg *slog.Logger) {
	e.meter.mu.Lock()
	e.meter.log = lg
	e.meter.mu.Unlock()
}

// UsageLog trả logger usage (có thể nil) để package khác ghi bất thường của
// chính nó (vd extract bỏ bớt cửa sổ) vào cùng file.
func (e *Egress) UsageLog() *slog.Logger {
	e.meter.mu.Lock()
	defer e.meter.mu.Unlock()
	return e.meter.log
}

// FlushUsage ghi dòng tổng hợp (nếu có call) rồi reset bộ đếm. Gọi khi
// process thoát; trong process dài, record tự flush mỗi usageFlushEvery.
func (e *Egress) FlushUsage() {
	e.meter.mu.Lock()
	defer e.meter.mu.Unlock()
	e.meter.flushLocked()
}

// FlushUsageIfDue: như FlushUsage nhưng chỉ khi đã quá usageFlushEvery —
// gọi định kỳ trong process dài để tổng hợp không kẹt khi không còn call.
func (e *Egress) FlushUsageIfDue() {
	e.meter.mu.Lock()
	defer e.meter.mu.Unlock()
	if nowFn().Sub(e.meter.lastFlush) >= usageFlushEvery {
		e.meter.flushLocked()
	}
}

func (m *meter) record(ctx context.Context, op, model string, pol Policy, ci callInfo, err error, mediaBytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.log == nil {
		return
	}
	k := aggKey{op, model, purposeOf(ctx)}
	a := m.aggs[k]
	if a == nil {
		a = &agg{}
		m.aggs[k] = a
	}
	a.calls++
	a.reqBytes += int64(ci.reqBytes)
	a.totalLatency += ci.latency
	a.maxLatency = max(a.maxLatency, ci.latency)
	p := ci.usage.prompt()
	a.prompt += int64(p)
	a.completion += int64(ci.usage.CompletionTokens)
	a.maxPrompt = max(a.maxPrompt, p)
	if ci.cached >= 0 {
		a.cacheReported++
		a.cachedSum += int64(ci.cached)
	}

	var reasons []string
	if err != nil && !errors.Is(err, context.Canceled) {
		a.errors++
		reasons = append(reasons, "error")
	}
	if p > anomalyPromptTokens || ci.reqBytes-mediaBytes > anomalyRequestBytes {
		a.big++
		reasons = append(reasons, "large_request")
	}
	if ci.latency > anomalyLatency {
		a.slow++
		reasons = append(reasons, "slow")
	}
	if err == nil && ci.status != 0 && !ci.hasUsage && m.seen[k] == 0 {
		reasons = append(reasons, "no_usage_field")
	}
	m.seen[k]++
	n := m.seen[k]

	attrs := []any{
		"op", op, "model", model, "policy", string(pol), "purpose", k.purpose,
		"req_bytes", ci.reqBytes, "resp_bytes", ci.respBytes, "status", ci.status,
		"latency_ms", ci.latency.Milliseconds(),
		"prompt_tokens", p, "completion_tokens", ci.usage.CompletionTokens, "cached_tokens", ci.cached,
	}
	switch {
	case len(reasons) > 0:
		attrs = append(attrs, "anomaly", reasons)
		if err != nil {
			attrs = append(attrs, "err", clipErr(err.Error()))
		}
		m.log.Warn("egress: bất thường", attrs...)
	case n == 1 || n%sampleEvery == 0:
		attrs = append(attrs, "sample_n", n)
		m.log.Info("egress: mẫu", attrs...)
	}
	if nowFn().Sub(m.lastFlush) >= usageFlushEvery {
		m.flushLocked()
	}
}

func (m *meter) flushLocked() {
	defer func() { m.lastFlush = nowFn() }()
	if m.log == nil || len(m.aggs) == 0 {
		return
	}
	keys := make([]aggKey, 0, len(m.aggs))
	for k := range m.aggs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].op != keys[j].op {
			return keys[i].op < keys[j].op
		}
		if keys[i].purpose != keys[j].purpose {
			return keys[i].purpose < keys[j].purpose
		}
		return keys[i].model < keys[j].model
	})
	for _, k := range keys {
		a := m.aggs[k]
		hit := -1.0 // gateway không trả field cache
		if a.cacheReported > 0 && a.prompt > 0 {
			hit = float64(a.cachedSum) / float64(a.prompt)
		}
		m.log.Info("egress: tổng hợp",
			"op", k.op, "model", k.model, "purpose", k.purpose,
			"since", m.lastFlush.Format(time.RFC3339),
			"calls", a.calls, "errors", a.errors, "large", a.big, "slow", a.slow,
			"req_bytes", a.reqBytes, "prompt_tokens", a.prompt, "completion_tokens", a.completion,
			"cached_tokens", a.cachedSum, "cache_reported_calls", a.cacheReported, "cache_hit_ratio", round3(hit),
			"max_prompt_tokens", a.maxPrompt,
			"avg_latency_ms", (a.totalLatency / time.Duration(a.calls)).Milliseconds(),
			"max_latency_ms", a.maxLatency.Milliseconds())
	}
	m.aggs = map[aggKey]*agg{}
}

func round3(f float64) float64 { return float64(int64(f*1000+0.5*sign(f))) / 1000 }

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// clipErr: error của doJSON có thể chứa tối đa 500 byte body lỗi gateway —
// cắt còn 300 để log gọn.
func clipErr(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
