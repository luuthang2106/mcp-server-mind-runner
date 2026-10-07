//go:build darwin

package docread

import (
	"context"
	"strings"
	"testing"

	"mind-runner/internal/execx"
)

// TestReadPDFKit: máy không có pdftotext vẫn đọc được PDF qua PDFKit (osascript).
func TestReadPDFKit(t *testing.T) {
	text, err := Read(context.Background(), noPdftotext{execx.OS{}}, "testdata/two-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, Marker("trang 1")) || !strings.Contains(text, Marker("trang 2")) {
		t.Fatalf("thiếu marker trang:\n%s", text)
	}
	if !strings.Contains(text, "Dòng 1: vector int8 tiết kiệm RAM.") || !strings.Contains(text, "Dòng 70:") {
		t.Fatalf("thiếu nội dung:\n%.300s", text)
	}
}
