package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mind-runner/internal/config"
)

func testCfg(baseURL string) config.Config {
	c := config.Default()
	c.Gateway.BaseURL = baseURL
	c.Gateway.APIKey = "test-key"
	c.Gateway.Models.Extract = "test-extract"
	return c
}

// TestEmbedBatchesAndOrder: 15 texts → đúng 2 request (10+5); data xáo index
// vẫn phải khớp thứ tự input.
func TestEmbedBatchesAndOrder(t *testing.T) {
	var calls atomic.Int32
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path=%s", r.URL.Path)
		}
		calls.Add(1)
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		batchSizes = append(batchSizes, len(req.Input))
		// trả data xáo ngược index
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[len(req.Input)-1-i] = map[string]any{"index": i, "embedding": []float32{float32(i), 1}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	eg := New(testCfg(srv.URL), nil)
	texts := make([]string, 15)
	for i := range texts {
		texts[i] = "t" + strings.Repeat("x", i)
	}
	vecs, err := eg.Embed(context.Background(), PolicyCloud, texts)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, muốn 2", calls.Load())
	}
	if len(batchSizes) != 2 || batchSizes[0] != 10 || batchSizes[1] != 5 {
		t.Fatalf("batchSizes=%v", batchSizes)
	}
	if len(vecs) != 15 {
		t.Fatalf("vecs=%d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 2 || v[0] != float32(i%10) {
			t.Fatalf("vecs[%d]=%v, muốn[0]=%d (đúng thứ tự input)", i, v, i%10)
		}
	}
}

// TestEmbedHTTPError: 500 → error chứa status; data thiếu → error.
func TestEmbedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	eg := New(testCfg(srv.URL), nil)
	if _, err := eg.Embed(context.Background(), PolicyCloud, []string{"a"}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v, muốn chứa 500", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 2 input nhưng chỉ trả 1 vector
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1}}}})
	}))
	defer srv2.Close()
	eg2 := New(testCfg(srv2.URL), nil)
	if _, err := eg2.Embed(context.Background(), PolicyCloud, []string{"a", "b"}); err == nil {
		t.Fatal("thiếu vector phải lỗi")
	}
}

// TestChat: temperature 0, đúng messages, trả choices[0].message.content; auth header.
func TestChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth=%q", got)
		}
		var req struct {
			Temperature float64 `json:"temperature"`
			Messages    []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Temperature != 0 || len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "câu hỏi" {
			t.Errorf("req=%+v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "nội dung trả lời"}}},
		})
	}))
	defer srv.Close()

	eg := New(testCfg(srv.URL), nil)
	got, err := eg.Chat(context.Background(), PolicyCloud, "hệ thống", "câu hỏi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nội dung trả lời" {
		t.Fatalf("got=%q", got)
	}
}

// TestRerank: model rỗng → error không gọi mạng; shape phẳng thật (verified
// trên gateway thật 2026-10-06): {model,query,documents} → {results:[{index,relevance_score}]};
// kết quả xáo index vẫn map đúng thứ tự docs.
func TestRerank(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/rerank" {
			t.Errorf("path=%s", r.URL.Path)
		}
		var req struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Model != "qwen3-rerank" || req.Query != "bộ nhớ" || len(req.Documents) != 2 {
			t.Errorf("req=%+v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"index": 1, "relevance_score": 0.2, "document": map[string]any{"text": req.Documents[1]}},
			{"index": 0, "relevance_score": 0.9, "document": map[string]any{"text": req.Documents[0]}},
		}})
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	cfg.Gateway.Models.Rerank = ""
	eg := New(cfg, nil)
	if _, err := eg.Rerank(context.Background(), PolicyCloud, "bộ nhớ", []string{"a", "b"}); err == nil {
		t.Fatal("model rỗng phải lỗi")
	}
	if calls.Load() != 0 {
		t.Fatalf("không được gọi mạng khi thiếu model (calls=%d)", calls.Load())
	}

	cfg.Gateway.Models.Rerank = "qwen3-rerank"
	eg = New(cfg, nil)
	scores, err := eg.Rerank(context.Background(), PolicyCloud, "bộ nhớ", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.2 {
		t.Fatalf("scores=%v, muốn [0.9 0.2] theo thứ tự docs", scores)
	}
}

// TestRedactInPayloads: secret không rời máy — request body của cả 3 endpoint
// chỉ chứa marker [REDACTED:...], không chứa secret gốc; response vẫn parse
// bình thường; log chỉ có số lượng + tên rule, không có nội dung.
func TestRedactInPayloads(t *testing.T) {
	const (
		secret = "sk-abc123XYZ_def-456ghi789"
		text   = "OPENAI_API_KEY=" + secret + " nhớ nhé"
	)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		bodies = append(bodies, string(b))
		switch r.URL.Path {
		case "/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1, 2}}}})
		case "/rerank":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 0, "relevance_score": 0.5}}})
		case "/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}}})
		default:
			t.Errorf("path=%s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	eg := New(testCfg(srv.URL), nil)
	if _, err := eg.Embed(context.Background(), PolicyCloud, []string{text}); err != nil {
		t.Fatalf("Embed parse response lỗi: %v", err)
	}
	if _, err := eg.Rerank(context.Background(), PolicyCloud, secret, []string{text}); err != nil {
		t.Fatalf("Rerank parse response lỗi: %v", err)
	}
	if _, err := eg.Chat(context.Background(), PolicyCloud, "system: password=hunter2hunter2hunter2", "user "+secret); err != nil {
		t.Fatalf("Chat parse response lỗi: %v", err)
	}

	if len(bodies) != 3 {
		t.Fatalf("bodies=%d, muốn 3", len(bodies))
	}
	for i, b := range bodies {
		if strings.Contains(b, secret) {
			t.Fatalf("body %d còn secret gốc: %s", i, b)
		}
		if !strings.Contains(b, "[REDACTED:") {
			t.Fatalf("body %d thiếu marker: %s", i, b)
		}
	}
	logs := buf.String()
	for _, rule := range []string{"api-key", "secret"} {
		if !strings.Contains(logs, rule) {
			t.Fatalf("log thiếu tên rule %q: %s", rule, logs)
		}
	}
	if strings.Contains(logs, secret) || strings.Contains(logs, "hunter2") {
		t.Fatalf("log chứa nội dung: %s", logs)
	}
}
