package egress

import (
	"testing"
)

// TestRedactTable: mỗi rule khớp → marker đúng + Counts đúng; không khớp giả
// (token=abc <16 ký tự, api-key ngắn, text thường) nguyên vẹn; thứ tự áp dụng
// cụ thể-trước-generic không double count; chạy lại không nhân marker.
func TestRedactTable(t *testing.T) {
	const (
		privKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
		awsKey  = "AKIAIOSFODNN7EXAMPLE"
		apiKey  = "sk-abc123XYZ_def-456ghi789"
		ghToken = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
		bearer  = "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0"
	)
	cases := []struct {
		name  string
		in    string
		want  string
		count map[string]int
	}{
		{
			name:  "private-key",
			in:    "trước\n" + privKey + "\nsau",
			want:  "trước\n[REDACTED:private-key]\nsau",
			count: map[string]int{"private-key": 1},
		},
		{
			name:  "aws-key",
			in:    "aws " + awsKey + " hết",
			want:  "aws [REDACTED:aws-key] hết",
			count: map[string]int{"aws-key": 1},
		},
		{
			name:  "api-key-cu-the-truoc-generic",
			in:    "OPENAI_API_KEY=" + apiKey,
			want:  "OPENAI_API_KEY=[REDACTED:api-key]",
			count: map[string]int{"api-key": 1},
		},
		{
			name:  "github-token",
			in:    "gh:" + ghToken + "!",
			want:  "gh:[REDACTED:github-token]!",
			count: map[string]int{"github-token": 1},
		},
		{
			name:  "bearer",
			in:    "Authorization: " + bearer,
			want:  "Authorization: Bearer [REDACTED:bearer]",
			count: map[string]int{"bearer": 1},
		},
		{
			name:  "secret-password",
			in:    "password: hunter2hunter2hunter2",
			want:  "password: [REDACTED:secret]",
			count: map[string]int{"secret": 1},
		},
		{
			name:  "secret-api-key-quotes",
			in:    `api_key="abcdefghijklmnop1234"`,
			want:  `api_key="[REDACTED:secret]"`,
			count: map[string]int{"secret": 1},
		},
		{
			name:  "nhieu-rule-trong-mot-text",
			in:    "k1:" + apiKey + " " + bearer + " password=hunter2hunter2hunter2",
			want:  "k1:[REDACTED:api-key] Bearer [REDACTED:bearer] password=[REDACTED:secret]",
			count: map[string]int{"api-key": 1, "bearer": 1, "secret": 1},
		},
		{
			name:  "cung-rule-hai-lan",
			in:    apiKey + " và " + apiKey,
			want:  "[REDACTED:api-key] và [REDACTED:api-key]",
			count: map[string]int{"api-key": 2},
		},
		{
			name:  "secret-password-ngan",
			in:    "password: Hunter2!x rồi",
			want:  "password: [REDACTED:secret] rồi",
			count: map[string]int{"secret": 1},
		},
		{
			name:  "secret-mat-khau",
			in:    "Mật khẩu: abc123\nmat khau=qwerty9\nDB_PASS=s3cr3t!",
			want:  "Mật khẩu: [REDACTED:secret]\nmat khau=[REDACTED:secret]\nDB_PASS=[REDACTED:secret]",
			count: map[string]int{"secret": 3},
		},
		{
			name:  "secret-json",
			in:    `{"password": "Hunter2!x"}`,
			want:  `{"password": "[REDACTED:secret]"}`,
			count: map[string]int{"secret": 1},
		},
		{
			name:  "khong-khop-prose-password",
			in:    "Sửa password reset flow, passport còn hạn.",
			want:  "Sửa password reset flow, passport còn hạn.",
			count: map[string]int{},
		},
		{
			name:  "private-key-thieu-end",
			in:    "trước\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0B",
			want:  "trước\n[REDACTED:private-key]",
			count: map[string]int{"private-key": 1},
		},
		{
			name:  "private-key-thieu-begin",
			in:    "AQEFAASCBKcwggSjAgEAAoIBAQC7\n-----END PRIVATE KEY-----\nsau",
			want:  "[REDACTED:private-key]\nsau",
			count: map[string]int{"private-key": 1},
		},
		{
			name:  "khong-khop-gia-token-ngan",
			in:    "token=abc",
			want:  "token=abc",
			count: map[string]int{},
		},
		{
			name:  "khong-khop-gia-api-key-ngan",
			in:    "sk-ngan",
			want:  "sk-ngan",
			count: map[string]int{},
		},
		{
			name:  "text-thuong-nguyen-ven",
			in:    "Hôm nay họp review spec mind-runner lúc 9h.",
			want:  "Hôm nay họp review spec mind-runner lúc 9h.",
			count: map[string]int{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if got.Text != tc.want {
				t.Fatalf("Text=%q\nmuốn  %q", got.Text, tc.want)
			}
			if len(got.Counts) != len(tc.count) {
				t.Fatalf("Counts=%v, muốn %v", got.Counts, tc.count)
			}
			for rule, n := range tc.count {
				if got.Counts[rule] != n {
					t.Fatalf("Counts[%s]=%d, muốn %d (Counts=%v)", rule, got.Counts[rule], n, got.Counts)
				}
			}
			// idempotent: chạy lại trên chính kết quả không được nhân marker
			again := Redact(got.Text)
			if again.Text != got.Text || len(again.Counts) != 0 {
				t.Fatalf("không idempotent: Text=%q Counts=%v", again.Text, again.Counts)
			}
		})
	}
}
