package store

import (
	"strings"
	"unicode"
)

// viFold: chữ tiếng Việt có dấu → không dấu (đủ cho so khớp tiêu đề việc).
var viFold = func() map[rune]rune {
	groups := map[rune]string{
		'a': "àáạảãâầấậẩẫăằắặẳẵ", 'e': "èéẹẻẽêềếệểễ", 'i': "ìíịỉĩ",
		'o': "òóọỏõôồốộổỗơờớợởỡ", 'u': "ùúụủũưừứựửữ", 'y': "ỳýỵỷỹ", 'd': "đ",
	}
	m := map[rune]rune{}
	for base, s := range groups {
		for _, r := range s {
			m[r] = base
		}
	}
	return m
}()

// NormTitle chuẩn hoá tiêu đề để nhận ra việc trùng: chữ thường, bỏ dấu,
// bỏ dấu câu, gộp khoảng trắng. "Gửi báo giá!" == "gui bao gia".
func NormTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if f, ok := viFold[r]; ok {
			r = f
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		} else {
			space = true
		}
	}
	return b.String()
}
