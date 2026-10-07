package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("dòng log không phải JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func byMsg(lines []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["msg"] == msg {
			out = append(out, l)
		}
	}
	return out
}

// TestUsageLogSamplingAnomalyAggregate: call đầu được lấy mẫu, call bình
// thường sau đó im lặng, lỗi/request lớn luôn ghi; tổng hợp đủ token + tỉ lệ
// cache; tuyệt đối không có nội dung request trong log.
func TestUsageLogSamplingAnomalyAggregate(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],
			"usage":{"prompt_tokens":1000,"completion_tokens":20,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":200}}`))
	}))
	defer srv.Close()
	e := New(testCfg(srv.URL), nil)
	var buf bytes.Buffer
	e.SetUsageLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	ctx := WithPurpose(context.Background(), "extract_session")

	for i := 0; i < 5; i++ {
		if _, err := e.Chat(ctx, PolicyCloud, "sys", "BÍ-MẬT-NỘI-DUNG"); err != nil {
			t.Fatal(err)
		}
	}
	old := anomalyRequestBytes
	anomalyRequestBytes = 10
	defer func() { anomalyRequestBytes = old }()
	e.Chat(ctx, PolicyCloud, "sys", "lớn") // large_request
	anomalyRequestBytes = old
	fail = true
	e.Chat(ctx, PolicyCloud, "sys", "x") // error
	e.FlushUsage()

	if strings.Contains(buf.String(), "BÍ-MẬT") {
		t.Fatal("log chứa nội dung request")
	}
	lines := logLines(t, &buf)
	samples := byMsg(lines, "egress: mẫu")
	if len(samples) != 1 || samples[0]["cached_tokens"].(float64) != 800 || samples[0]["purpose"] != "extract_session" {
		t.Fatalf("samples=%v", samples)
	}
	anom := byMsg(lines, "egress: bất thường")
	if len(anom) != 2 || !strings.Contains(anom[1]["err"].(string), "429") {
		t.Fatalf("anomalies=%v", anom)
	}
	agg := byMsg(lines, "egress: tổng hợp")
	if len(agg) != 1 {
		t.Fatalf("agg=%v", agg)
	}
	a := agg[0]
	if a["calls"].(float64) != 7 || a["errors"].(float64) != 1 || a["large"].(float64) != 1 ||
		a["prompt_tokens"].(float64) != 6000 || a["cached_tokens"].(float64) != 4800 || a["cache_hit_ratio"].(float64) != 0.8 {
		t.Fatalf("agg=%v", a)
	}
	// flush lần 2 không có call → không ghi gì thêm
	n := len(lines)
	e.FlushUsage()
	if len(logLines(t, &buf)) != n {
		t.Fatal("flush rỗng vẫn ghi")
	}
}

// TestUsageNoCacheField: gateway không trả field cache → cached=-1 và
// ratio=-1 (phân biệt với "có trả nhưng 0"); thiếu cả usage → cờ no_usage_field.
func TestUsageNoCacheField(t *testing.T) {
	withUsage := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if withUsage {
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	e := New(testCfg(srv.URL), nil)
	var buf bytes.Buffer
	e.SetUsageLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	e.Chat(context.Background(), PolicyCloud, "s", "u")
	e.FlushUsage()
	lines := logLines(t, &buf)
	if s := byMsg(lines, "egress: mẫu"); len(s) != 1 || s[0]["cached_tokens"].(float64) != -1 {
		t.Fatalf("samples=%v", s)
	}
	if a := byMsg(lines, "egress: tổng hợp"); a[0]["cache_hit_ratio"].(float64) != -1 || a[0]["purpose"] != "-" {
		t.Fatalf("agg=%v", a)
	}

	withUsage = false
	buf.Reset()
	e2 := New(testCfg(srv.URL), nil)
	e2.SetUsageLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	e2.Chat(context.Background(), PolicyCloud, "s", "u")
	if a := byMsg(logLines(t, &buf), "egress: bất thường"); len(a) != 1 || !strings.Contains(buf.String(), "no_usage_field") {
		t.Fatalf("log=%s", buf.String())
	}
}

// TestUsageHourlyFlush: process dài — record tự ghi tổng hợp khi quá 1 giờ.
func TestUsageHourlyFlush(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer srv.Close()
	clock := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	oldNow := nowFn
	nowFn = func() time.Time { return clock }
	defer func() { nowFn = oldNow }()

	e := New(testCfg(srv.URL), nil)
	var buf bytes.Buffer
	e.SetUsageLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	e.Chat(context.Background(), PolicyCloud, "s", "u")
	e.FlushUsageIfDue()
	if len(byMsg(logLines(t, &buf), "egress: tổng hợp")) != 0 {
		t.Fatal("flush sớm")
	}
	clock = clock.Add(61 * time.Minute)
	e.Chat(context.Background(), PolicyCloud, "s", "u")
	agg := byMsg(logLines(t, &buf), "egress: tổng hợp")
	if len(agg) != 1 || agg[0]["calls"].(float64) != 2 || agg[0]["cached_tokens"].(float64) != 8 {
		t.Fatalf("agg=%v", agg)
	}
}
