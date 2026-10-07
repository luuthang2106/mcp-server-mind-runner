// Package egressfake là helper test dùng chung (M3–M6): httptest.Server giả
// gateway/endpoint local, đếm số lần gọi từng endpoint.
package egressfake

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"mind-runner/internal/config"
	"mind-runner/internal/egress"
)

type Options struct {
	EmbedVec  func(text string) []float32                 // mặc định: sha256(text) → 8 chiều, giá trị −1..1
	Rerank    func(query string, docs []string) []float64 // mặc định: điểm giảm dần theo thứ tự docs
	ChatResp  func(system, user string) string            // mặc định: "{}"
	OmniResp  func(parts []map[string]any) string         // mặc định: "{}" (parts = content array đa hình)
	EmbedErr  error                                       // != nil → /embeddings trả 500 (gateway hỏng)
	RerankErr error                                       // != nil → /rerank trả 500
}

type Fake struct {
	Egress      *egress.Egress
	URL         string // base URL của httptest.Server (để test tự dựng egress qua config)
	EmbedCalls  int
	RerankCalls int
	ChatCalls   int
	OmniCalls   int
	OmniModel   string           // model của request omni gần nhất
	OmniParts   []map[string]any // content array của request omni gần nhất

	srv       *httptest.Server
	closeOnce sync.Once
}

// Close đóng server sớm để test endpoint chết; idempotent (t.Cleanup gọi lại).
func (f *Fake) Close() { f.closeOnce.Do(f.srv.Close) }

// LocalModels: bộ model giả cho [spaces.local.models] khi test policy local.
func LocalModels() config.Models {
	return config.Models{Embed: "fake-local-embed", Extract: "fake-local-extract",
		Rerank: "fake-local-rerank", Omni: "fake-local-omni"}
}

// Config ghép cloud + local fake thành config đủ cho egress hai policy;
// local nil → [spaces.local] chưa cấu hình (policy local sẽ lỗi rõ).
func Config(cloud, local *Fake) config.Config {
	cfg := config.Default()
	cfg.Gateway.BaseURL = cloud.URL
	cfg.Gateway.APIKey = "fake"
	cfg.Gateway.Models.Extract = "fake-extract"
	cfg.Gateway.Models.Omni = "fake-omni"
	if local != nil {
		cfg.Spaces.Local.BaseURL = local.URL
		cfg.Spaces.Local.Models = LocalModels()
	}
	return cfg
}

// New: fake cloud (hành vi M3 giữ nguyên).
func New(t *testing.T, opt Options) *Fake { return newFake(t, opt, false) }

// NewLocal: fake thứ hai cho endpoint [spaces.local] của policy local.
func NewLocal(t *testing.T, opt Options) *Fake { return newFake(t, opt, true) }

func newFake(t *testing.T, opt Options, local bool) *Fake {
	t.Helper()
	f := &Fake{}
	embedVec := opt.EmbedVec
	if embedVec == nil {
		embedVec = func(text string) []float32 {
			sum := sha256.Sum256([]byte(text))
			v := make([]float32, 8)
			for i := range v {
				v[i] = float32(sum[i])/127.5 - 1
			}
			return v
		}
	}
	rerank := opt.Rerank
	if rerank == nil {
		rerank = func(_ string, docs []string) []float64 {
			s := make([]float64, len(docs))
			for i := range s {
				s[i] = float64(len(docs) - i)
			}
			return s
		}
	}
	chatResp := opt.ChatResp
	if chatResp == nil {
		chatResp = func(string, string) string { return "{}" }
	}
	omniResp := opt.OmniResp
	if omniResp == nil {
		omniResp = func([]map[string]any) string { return "{}" }
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embeddings":
			f.EmbedCalls++
			if opt.EmbedErr != nil {
				http.Error(w, opt.EmbedErr.Error(), http.StatusInternalServerError)
				return
			}
			var req struct {
				Input []string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			data := make([]map[string]any, len(req.Input))
			for i, text := range req.Input {
				data[i] = map[string]any{"index": i, "embedding": embedVec(text)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/rerank":
			f.RerankCalls++
			if opt.RerankErr != nil {
				http.Error(w, opt.RerankErr.Error(), http.StatusInternalServerError)
				return
			}
			var req struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			scores := rerank(req.Query, req.Documents)
			results := make([]map[string]any, len(scores))
			for i, s := range scores {
				results[i] = map[string]any{"index": i, "relevance_score": s}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case "/chat/completions":
			// content dạng mảng = omni (đa hình); dạng chuỗi = chat text.
			var req struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.Messages) > 0 && len(req.Messages[0].Content) > 0 && req.Messages[0].Content[0] == '[' {
				f.OmniCalls++
				f.OmniModel = req.Model
				f.OmniParts = nil
				var parts []map[string]any
				_ = json.Unmarshal(req.Messages[0].Content, &parts)
				f.OmniParts = parts
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": omniResp(parts)}}},
				})
				return
			}
			f.ChatCalls++
			var system, user string
			for _, m := range req.Messages {
				var text string
				_ = json.Unmarshal(m.Content, &text)
				switch m.Role {
				case "system":
					system = text
				case "user":
					user = text
				}
			}
			content := chatResp(system, user)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": content}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	f.srv = srv
	t.Cleanup(f.Close)

	cfg := config.Default()
	cfg.Gateway.Models.Extract = "fake-extract" // config thật luôn có model extract
	cfg.Gateway.Models.Omni = "fake-omni"
	if local {
		cfg.Spaces.Local.BaseURL = srv.URL
		cfg.Spaces.Local.Models = LocalModels()
	} else {
		cfg.Gateway.BaseURL = srv.URL
		cfg.Gateway.APIKey = "fake"
	}
	f.Egress = egress.New(cfg, nil)
	f.URL = srv.URL
	return f
}
