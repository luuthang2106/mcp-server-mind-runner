//go:build eval

// TestEvalExtract: harness đo chất lượng trích xuất trên phiên thật.
//
// Fixtures ở evals/extract/: <short8>.jsonl là raw transcript JSONL (đã giải
// nén) của một phiên Claude Code thật; wants.json là các ý chính dán nhãn tay
// mà kết quả trích xuất phải chứa (so khớp sau khi chuẩn hoá như chống trùng
// task). Dữ liệu phiên thật nên cả thư mục không commit — repo public, xem
// .gitignore. Chạy: make eval-extract
//
// Mỗi phiên chạy trong DB tạm riêng qua đúng pipeline RunSession. Recall =
// số want được ít nhất một note/việc chứa / tổng want. Các mục không chứa
// want nào được in ra để soi nhiễu.
package extract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/extract"
	"mind-runner/internal/store"
)

func TestEvalExtract(t *testing.T) {
	if os.Getenv("MIND_RUNNER_EVAL") != "1" {
		t.Skip("đặt MIND_RUNNER_EVAL=1 để chạy eval (make eval-extract)")
	}
	dir := filepath.Join("..", "..", "evals", "extract")
	wantsRaw, err := os.ReadFile(filepath.Join(dir, "wants.json"))
	if err != nil {
		t.Skipf("chưa có %s — bộ fixtures giữ local, xem README (mục Development)", dir)
	}
	var wantsBySession map[string][]string
	if err := json.Unmarshal(wantsRaw, &wantsBySession); err != nil {
		t.Fatal(err)
	}
	fixtures, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(fixtures)
	if len(fixtures) == 0 {
		t.Skipf("%s chưa có fixture .jsonl — bộ mẫu giữ local", dir)
	}

	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.APIKey == "" || cfg.Gateway.BaseURL == "" {
		t.Skipf("thiếu gateway api_key/base_url — export MIND_RUNNER_GATEWAY_API_KEY + "+
			"MIND_RUNNER_GATEWAY_BASE_URL rồi chạy lại (config %s)", config.DefaultConfigPath())
	}

	ctx := context.Background()
	matched, total := 0, 0
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		wants := wantsBySession[name]
		if len(wants) == 0 {
			t.Fatalf("wants.json thiếu phiên %q", name)
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		st := newStore(t)
		seedSessionRaw(t, st, name, gzipBlob(t, string(raw)))
		// Client 10 phút: harness đo chất lượng trích xuất, không đo độ trễ.
		// Production dùng mặc định 120s — một cửa sổ 40k runes có thể vượt
		// ngưỡng đó khi gateway chậm; job sẽ retry (xem jobs.attempts).
		eg := egress.New(cfg, &http.Client{Timeout: 10 * time.Minute})
		b := brain.New(st, eg, &cfg)
		if err := extract.New(st, b, eg, &cfg).RunSession(ctx, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		items := collectItems(t, st)
		norm := make([]string, len(items))
		for i, it := range items {
			norm[i] = store.NormTitle(it)
		}
		nwants := make([]string, len(wants))
		for i, w := range wants {
			nwants[i] = store.NormTitle(w)
		}

		hit := 0
		for i, w := range wants {
			found := false
			for _, it := range norm {
				if strings.Contains(it, nwants[i]) {
					found = true
					break
				}
			}
			if found {
				hit++
			} else {
				t.Logf("MISS [%s] %q", name, w)
			}
		}
		var noise []string
		for i, it := range norm {
			if !containsAny(it, nwants) {
				noise = append(noise, items[i])
			}
		}
		matched += hit
		total += len(wants)
		t.Logf("%-10s recall %d/%d · trích ra %d mục, %d ngoài want", name, hit, len(wants), len(items), len(noise))
		for i, s := range noise {
			if i >= 8 {
				t.Logf("  … còn %d mục ngoài want", len(noise)-8)
				break
			}
			t.Logf("  ngoài want: %s", clipRunes(s, 110))
		}
	}

	mean := float64(matched) / float64(total)
	t.Logf("=== extract recall = %.3f (%d/%d want, %d phiên)", mean, matched, total, len(fixtures))
	if mean < 0.7 {
		t.Fatalf("extract recall %.3f < 0.7 — xem các dòng MISS phía trên", mean)
	}
}

// collectItems: mọi note do extract ghi và mọi task vừa tạo (DB tạm chỉ có dữ liệu phiên này).
func collectItems(t *testing.T, st *store.Store) []string {
	t.Helper()
	var items []string
	rows, err := st.DB().Query(`SELECT text FROM notes WHERE source='hook:stop' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		items = append(items, s)
	}
	trows, err := st.DB().Query(`SELECT title FROM tasks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer trows.Close()
	for trows.Next() {
		var s string
		if err := trows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		items = append(items, s)
	}
	return items
}

func containsAny(norm string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(norm, n) {
			return true
		}
	}
	return false
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
