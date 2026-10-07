// Package egress là choke point duy nhất ra ngoài (gateway OpenAI-compatible hoặc
// endpoint local). Mọi text rời máy đều bị redact tại đây.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mind-runner/internal/config"
)

const embedBatch = 10

// Policy quyết định endpoint nào nhận request. KHÔNG có fallback ngầm giữa
// hai policy — thiếu cấu hình local thì lỗi rõ ràng, tuyệt đối không rơi
// sang cloud (spec §spaces policy).
type Policy string

const (
	PolicyCloud Policy = "cloud"
	PolicyLocal Policy = "local"
)

// ErrNoAPIKey: policy cloud nhưng chưa có API key (key chỉ đến từ env của MCP
// client — process khác như launchd/hook có thể thiếu). Trả trước mọi HTTP
// request để worker nhận diện và hoãn job thay vì đốt lượt retry.
var ErrNoAPIKey = errors.New("chưa có API key gateway (MIND_RUNNER_GATEWAY_API_KEY)")

type Egress struct {
	cfg   config.Config
	httpc *http.Client
	cloud *openaiClient
	local *openaiClient // nil khi [spaces.local].base_url chưa cấu hình
	meter *meter        // log usage (usage.go); tắt cho tới khi SetUsageLog
}

// New: httpc == nil → &http.Client{Timeout: 120s}.
func New(cfg config.Config, httpc *http.Client) *Egress {
	if httpc == nil {
		httpc = &http.Client{Timeout: 120 * time.Second}
	}
	e := &Egress{
		cfg:   cfg,
		httpc: httpc,
		meter: newMeter(),
		cloud: &openaiClient{base: strings.TrimSuffix(cfg.Gateway.BaseURL, "/"), key: cfg.Gateway.APIKey, httpc: httpc},
	}
	if cfg.Spaces.Local.BaseURL != "" {
		// Endpoint local (Ollama) không cần auth — không gửi key cloud sang đó.
		e.local = &openaiClient{base: strings.TrimSuffix(cfg.Spaces.Local.BaseURL, "/"), key: "", httpc: httpc}
	}
	return e
}

// route trả client + bộ model theo policy. KHÔNG có nhánh fallback nào ở đây:
// local thiếu cấu hình hoặc policy lạ → error để call site xử lý (retry/stages).
func (e *Egress) route(pol Policy) (*openaiClient, config.Models, error) {
	switch pol {
	case PolicyCloud:
		if e.cloud.key == "" {
			return nil, config.Models{}, fmt.Errorf("policy cloud: %w", ErrNoAPIKey)
		}
		return e.cloud, e.cfg.Gateway.Models, nil
	case PolicyLocal:
		if e.local == nil {
			return nil, config.Models{}, errors.New("policy local nhưng [spaces.local].base_url chưa cấu hình")
		}
		return e.local, e.cfg.Spaces.Local.Models, nil
	default:
		return nil, config.Models{}, fmt.Errorf("policy không hợp lệ: %q", pol)
	}
}

// EmbedModel trả model embed theo policy (embeddings keyed theo model).
func (e *Egress) EmbedModel(pol Policy) string {
	if pol == PolicyLocal {
		return e.cfg.Spaces.Local.Models.Embed
	}
	return e.cfg.Gateway.Models.Embed
}

// Embed gửi batch 10, ghép kết quả theo .index về đúng thứ tự texts; text
// được redact trước khi gửi.
func (e *Egress) Embed(ctx context.Context, pol Policy, texts []string) ([][]float32, error) {
	client, models, err := e.route(pol)
	if err != nil {
		return nil, err
	}
	if models.Embed == "" {
		return nil, fmt.Errorf("%s thiếu model embed", pol)
	}
	texts, counts := redactAll(texts)
	logRedactions("embed", counts)
	out := make([][]float32, 0, len(texts))
	dim := 0
	for i := 0; i < len(texts); i += embedBatch {
		end := min(i+embedBatch, len(texts))
		var resp struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		ci, err := client.doJSON(ctx, "/embeddings", map[string]any{
			"model": models.Embed,
			"input": texts[i:end],
		}, &resp)
		e.meter.record(ctx, "embed", models.Embed, pol, ci, err, 0)
		if err != nil {
			return nil, err
		}
		n := end - i
		if len(resp.Data) != n {
			return nil, fmt.Errorf("embeddings: nhận %d vector cho %d input", len(resp.Data), n)
		}
		ordered := make([][]float32, n)
		for _, d := range resp.Data {
			if d.Index < 0 || d.Index >= n {
				return nil, fmt.Errorf("embeddings: index %d ngoài khoảng [0,%d)", d.Index, n)
			}
			if ordered[d.Index] != nil {
				return nil, fmt.Errorf("embeddings: index %d trùng", d.Index)
			}
			if len(d.Embedding) == 0 {
				return nil, fmt.Errorf("embeddings: vector rỗng ở index %d", d.Index)
			}
			// mọi vector trong một lần gọi phải cùng số chiều
			if dim == 0 {
				dim = len(d.Embedding)
			} else if len(d.Embedding) != dim {
				return nil, fmt.Errorf("embeddings: index %d có %d chiều, muốn %d", d.Index, len(d.Embedding), dim)
			}
			ordered[d.Index] = d.Embedding
		}
		out = append(out, ordered...)
	}
	return out, nil
}

// Rerank trả score theo ĐÚNG thứ tự docs. Shape phẳng {{model,query,documents}}
// (verified trên gateway thật 2026-10-06 — shape DashScope lồng bị gateway từ chối).
// Query + docs được redact trước khi gửi.
func (e *Egress) Rerank(ctx context.Context, pol Policy, query string, docs []string) ([]float64, error) {
	client, models, err := e.route(pol)
	if err != nil {
		return nil, err
	}
	if models.Rerank == "" {
		return nil, fmt.Errorf("%s thiếu model rerank", pol)
	}
	q := Redact(query)
	docs, counts := redactAll(docs)
	for name, n := range q.Counts {
		counts[name] += n
	}
	logRedactions("rerank", counts)
	var resp struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	ci, err := client.doJSON(ctx, "/rerank", map[string]any{
		"model":     models.Rerank,
		"query":     q.Text,
		"documents": docs,
	}, &resp)
	e.meter.record(ctx, "rerank", models.Rerank, pol, ci, err, 0)
	if err != nil {
		return nil, err
	}
	if len(resp.Results) != len(docs) {
		return nil, fmt.Errorf("rerank: nhận %d kết quả cho %d docs", len(resp.Results), len(docs))
	}
	scores := make([]float64, len(docs))
	for _, r := range resp.Results {
		if r.Index < 0 || r.Index >= len(docs) {
			return nil, fmt.Errorf("rerank: index %d ngoài khoảng [0,%d)", r.Index, len(docs))
		}
		scores[r.Index] = r.RelevanceScore
	}
	return scores, nil
}

// Chat: temperature 0 → choices[0].message.content.
// System + user được redact trước khi gửi.
func (e *Egress) Chat(ctx context.Context, pol Policy, system, user string) (string, error) {
	client, models, err := e.route(pol)
	if err != nil {
		return "", err
	}
	if models.Extract == "" {
		return "", fmt.Errorf("%s thiếu model extract", pol)
	}
	sys := Redact(system)
	usr := Redact(user)
	counts := map[string]int{}
	for name, n := range sys.Counts {
		counts[name] += n
	}
	for name, n := range usr.Counts {
		counts[name] += n
	}
	logRedactions("chat", counts)
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	ci, err := client.doJSON(ctx, "/chat/completions", map[string]any{
		"model":       models.Extract,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": sys.Text},
			{"role": "user", "content": usr.Text},
		},
	}, &resp)
	e.meter.record(ctx, "chat", models.Extract, pol, ci, err, 0)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("chat: response không có choices")
	}
	return resp.Choices[0].Message.Content, nil
}
