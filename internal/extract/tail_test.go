package extract

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTailRunes: giữ n rune cuối, cắt đúng biên rune (tiếng Việt nhiều byte).
func TestTailRunes(t *testing.T) {
	if got, cut := tailRunes("abc", 5); got != "abc" || cut {
		t.Fatalf("got=%q cut=%v", got, cut)
	}
	s := strings.Repeat("ố", 10) + "cuối"
	got, cut := tailRunes(s, 6)
	if !cut || got != "ốốcuối" || !utf8.ValidString(got) {
		t.Fatalf("got=%q cut=%v", got, cut)
	}
}
