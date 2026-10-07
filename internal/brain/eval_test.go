//go:build eval

// TestEvalRetrieval: harness đo retrieval trên bộ golden corpus/queries, chạy
// thật qua gateway (make eval). Baseline ghi lại bên dưới là mốc so sánh khi
// đổi model/prompt.
//
// Baseline 2026-10-06: mean recall@10 = 1.000 | mean MRR = 1.000 — 52 queries,
// 40 note, embed text-embedding-v4 + rerank qwen3-rerank qua gateway (0 MISS).
package brain_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/store"
	"mind-runner/internal/worker"
)

type corpusNote struct {
	Key   string   `json:"key"`
	Kind  string   `json:"kind"`
	Space string   `json:"space"`
	Text  string   `json:"text"`
	Tags  []string `json:"tags"`
}

type evalQuery struct {
	Query    string   `json:"query"`
	Relevant []string `json:"relevant"`
}

func loadJSONL[T any](t *testing.T, path string) []T {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var out []T
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("%s:%d: %v", path, line, err)
		}
		out = append(out, v)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEvalRetrieval(t *testing.T) {
	if os.Getenv("MIND_RUNNER_EVAL") != "1" {
		t.Skip("đặt MIND_RUNNER_EVAL=1 để chạy eval (make eval)")
	}
	corpus := loadJSONL[corpusNote](t, filepath.Join("..", "..", "evals", "corpus.jsonl"))
	queries := loadJSONL[evalQuery](t, filepath.Join("..", "..", "evals", "queries.jsonl"))
	// chặn bộ golden bị co lại âm thầm
	if len(queries) < 50 {
		t.Fatalf("queries=%d, cần >= 50", len(queries))
	}

	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.APIKey == "" || cfg.Gateway.BaseURL == "" {
		t.Skipf("thiếu gateway api_key/base_url (config %s) — chạy `mind-runner setup` "+
			"hoặc export MIND_RUNNER_GATEWAY_API_KEY + MIND_RUNNER_GATEWAY_BASE_URL",
			config.DefaultConfigPath())
	}

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "eval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	eg := egress.New(cfg, nil)
	b := brain.New(st, eg, &cfg)

	// nạp corpus (note_id → key) rồi embed đến hết
	corpusKeys := map[string]bool{}
	keyByNote := map[int64]string{}
	for _, c := range corpus {
		corpusKeys[c.Key] = true
		spaceID, err := st.SpaceByName(ctx, c.Space)
		if err != nil {
			t.Fatalf("note %s: %v", c.Key, err)
		}
		res, err := b.WriteNote(ctx, brain.WriteParams{
			SpaceID: spaceID, Kind: c.Kind, Text: c.Text, Tags: c.Tags, Source: "test:eval",
		})
		if err != nil {
			t.Fatalf("note %s: %v", c.Key, err)
		}
		keyByNote[res.NoteID] = c.Key
	}
	for _, q := range queries {
		for _, k := range q.Relevant {
			if !corpusKeys[k] {
				t.Fatalf("query %q trỏ key lạ %q", q.Query, k)
			}
		}
	}
	reg := worker.NewRegistry()
	worker.RegisterAll(reg, worker.Deps{Store: st, Brain: b, Egress: eg})
	n, err := worker.Run(ctx, st, reg, 0, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var nEmb int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT c.note_id) FROM embeddings e JOIN chunks c ON c.id = e.chunk_id`).Scan(&nEmb); err != nil {
		t.Fatal(err)
	}
	t.Logf("corpus: %d note, %d job đã chạy, embeddings phủ %d note", len(corpus), n, nEmb)
	if nEmb != len(corpus) {
		t.Fatalf("embeddings phủ %d/%d note — có job embed lỗi, xem jobs.last_error", nEmb, len(corpus))
	}

	// recall từng query
	var sumRecall, sumMRR float64
	agg := map[string]map[string]int{"fts": {}, "vector": {}, "rerank": {}}
	for _, q := range queries {
		res, err := b.Recall(ctx, brain.RecallParams{Query: q.Query, Limit: 10})
		if err != nil {
			t.Fatalf("query %q: %v", q.Query, err)
		}
		for stage, v := range res.Stages {
			agg[stage][v]++
		}
		relevant := map[string]bool{}
		for _, k := range q.Relevant {
			relevant[k] = true
		}
		hits, mrr := 0, 0.0
		for rank, h := range res.Hits {
			if relevant[keyByNote[h.NoteID]] {
				hits++
				if mrr == 0 {
					mrr = 1 / float64(rank+1)
				}
			}
		}
		recall := float64(hits) / float64(len(q.Relevant))
		sumRecall += recall
		sumMRR += mrr
		mark := "ok  "
		if recall < 1 {
			mark = "MISS"
		}
		t.Logf("%s recall@10=%.2f mrr=%.2f  %s", mark, recall, mrr, q.Query)
	}
	meanRecall := sumRecall / float64(len(queries))
	meanMRR := sumMRR / float64(len(queries))
	t.Logf("=== mean recall@10 = %.3f | mean MRR = %.3f | queries = %d", meanRecall, meanMRR, len(queries))
	for _, stage := range []string{"fts", "vector", "rerank"} {
		t.Logf("stages[%s] = %v", stage, agg[stage])
	}
	if meanRecall < 0.7 {
		t.Fatalf("mean recall@10 = %.3f < 0.7 — xem bảng MISS phía trên", meanRecall)
	}
}
