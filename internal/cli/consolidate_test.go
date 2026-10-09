package cli

import (
	"testing"
	"time"
)

// TestConsolidateDue: chưa có mốc (lần đầu) hoặc mốc hỏng → chạy ngay; đủ
// every_days → chạy; chưa đủ → không.
func TestConsolidateDue(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last string
		want bool
	}{
		{"", true}, // lần đầu
		{"rác", true},
		{now.AddDate(0, 0, -6).Format(time.RFC3339), false},
		{now.AddDate(0, 0, -7).Format(time.RFC3339), true},
	}
	for _, c := range cases {
		if got := consolidateDue(c.last, 7, now); got != c.want {
			t.Errorf("consolidateDue(%q)=%v, muốn %v", c.last, got, c.want)
		}
	}
}
