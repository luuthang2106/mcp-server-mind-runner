package egress

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// RedactResult: Text sau redact + số lần khớp của từng rule (không có nội dung).
type RedactResult struct {
	Text   string
	Counts map[string]int
}

type redactRule struct {
	name string
	re   *regexp.Regexp
	repl string
}

// redactRules áp dụng theo thứ tự CỤ THỂ trước, GENERIC sau (api-key phải chạy
// trước secret để "sk-..." không bị nuốt thành giá trị thô của rule secret).
// Marker "[REDACTED:x]" cố ý không khớp lại chính rule (bracket không nằm
// trong char class / không thoả separator) → Redact idempotent.
var redactRules = []redactRule{
	{
		name: "private-key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
		repl: "[REDACTED:private-key]",
	},
	// key bị cắt (thiếu END hoặc thiếu BEGIN): xoá tới cuối / từ đầu text.
	{
		name: "private-key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*\z`),
		repl: "[REDACTED:private-key]",
	},
	{
		name: "private-key",
		re:   regexp.MustCompile(`(?s)\A.*?-----END [A-Z ]*PRIVATE KEY-----`),
		repl: "[REDACTED:private-key]",
	},
	{
		name: "aws-key",
		re:   regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		repl: "[REDACTED:aws-key]",
	},
	{
		name: "api-key",
		re:   regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		repl: "[REDACTED:api-key]",
	},
	{
		name: "github-token",
		re:   regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
		repl: "[REDACTED:github-token]",
	},
	{
		name: "bearer",
		re:   regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{20,}`),
		repl: "Bearer [REDACTED:bearer]",
	},
	{
		name: "secret",
		// cần separator : hoặc = ("password reset flow" không khớp); giá trị ≥6 ký tự
		// không space/quote, không mở đầu bằng '[' (marker) → idempotent.
		re:   regexp.MustCompile(`(?i)(api[_-]?key|token|secret|pass(?:word|wd|phrase)?|pwd|m[aậ]t[ _]?kh[aẩ]u)(["']?\s*[:=]\s*["']?)([^\s"'\[][^\s"']{5,})`),
		repl: "$1$2[REDACTED:secret]",
	},
}

// Redact thay secret trong text bằng marker; Counts chỉ chứa rule khớp ≥1 lần.
func Redact(s string) RedactResult {
	counts := map[string]int{}
	text := s
	for _, r := range redactRules {
		if n := len(r.re.FindAllStringSubmatchIndex(text, -1)); n > 0 {
			text = applyRule(r, text)
			counts[r.name] += n
		}
	}
	return RedactResult{Text: text, Counts: counts}
}

// applyRule thay mọi match bằng repl, hỗ trợ template $N (ExpandString —
// ReplaceAllStringFunc không nhóm được).
func applyRule(r redactRule, s string) string {
	idx := r.re.FindAllStringSubmatchIndex(s, -1)
	var b []byte
	last := 0
	for _, m := range idx {
		b = append(b, s[last:m[0]]...)
		b = r.re.ExpandString(b, r.repl, s, m)
		last = m[1]
	}
	b = append(b, s[last:]...)
	return string(b)
}

// redactAll redact từng text, gộp Counts của cả lô.
func redactAll(ss []string) ([]string, map[string]int) {
	out := make([]string, len(ss))
	total := map[string]int{}
	for i, s := range ss {
		r := Redact(s)
		out[i] = r.Text
		for name, n := range r.Counts {
			total[name] += n
		}
	}
	return out, total
}

// logRedactions ghi CHỈ số lượng + tên rule — không bao giờ log nội dung.
func logRedactions(op string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	names := make([]string, 0, len(counts))
	total := 0
	for name, n := range counts {
		names = append(names, fmt.Sprintf("%s=%d", name, n))
		total += n
	}
	sort.Strings(names)
	slog.Info("egress: redact trước khi gửi", "op", op, "total", total, "rules", strings.Join(names, ","))
}
