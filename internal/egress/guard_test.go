package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestEmbedValidatesData: index trùng, vector rỗng, lệch chiều → lỗi, không trả nil vector.
func TestEmbedValidatesData(t *testing.T) {
	cases := map[string][]map[string]any{
		"trung-index": {{"index": 0, "embedding": []float32{1}}, {"index": 0, "embedding": []float32{2}}},
		"vector-rong": {{"index": 0, "embedding": []float32{1}}, {"index": 1, "embedding": []float32{}}},
		"lech-chieu":  {{"index": 0, "embedding": []float32{1, 2}}, {"index": 1, "embedding": []float32{1}}},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer srv.Close()
			if _, err := New(testCfg(srv.URL), nil).Embed(context.Background(), PolicyCloud, []string{"a", "b"}); err == nil {
				t.Fatal("muốn lỗi")
			}
		})
	}
}

// TestResponseTooLarge: body vượt trần → lỗi rõ "quá lớn", không phải lỗi parse JSON.
func TestResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"x":"` + strings.Repeat("a", maxResponseBytes) + `"}`))
	}))
	defer srv.Close()
	_, err := New(testCfg(srv.URL), nil).Chat(context.Background(), PolicyCloud, "s", "u")
	if err == nil || !strings.Contains(err.Error(), "quá lớn") {
		t.Fatalf("err=%v", err)
	}
}

// TestNoAPIKeyFailsFast: cloud thiếu key → ErrNoAPIKey trước mọi HTTP request.
func TestNoAPIKeyFailsFast(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	cfg.Gateway.APIKey = ""
	cfg.Gateway.Models.Embed, cfg.Gateway.Models.Rerank, cfg.Gateway.Models.Omni = "e", "r", "o"
	eg := New(cfg, nil)
	ctx := context.Background()
	_, e1 := eg.Embed(ctx, PolicyCloud, []string{"a"})
	_, e2 := eg.Rerank(ctx, PolicyCloud, "q", []string{"d"})
	_, e3 := eg.Chat(ctx, PolicyCloud, "s", "u")
	_, e4 := eg.Omni(ctx, PolicyCloud, OmniRequest{Text: "x"})
	for i, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, ErrNoAPIKey) {
			t.Fatalf("call %d: err=%v, muốn ErrNoAPIKey", i, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("đã gửi %d request dù thiếu key", hits.Load())
	}
}
