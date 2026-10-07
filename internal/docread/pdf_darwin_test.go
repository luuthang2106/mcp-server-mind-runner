//go:build darwin

package docread

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"mind-runner/internal/execx"
)

// noPdftotext: giả lập máy không cài poppler.
type noPdftotext struct{ execx.Runner }

func (r noPdftotext) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "pdftotext" {
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	return r.Runner.Run(ctx, name, args...)
}

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
