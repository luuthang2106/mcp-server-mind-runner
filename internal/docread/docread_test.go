package docread

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/execx"
)

type fakeRun struct {
	out   string
	calls []string
}

func (f *fakeRun) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return []byte(f.out), nil
}

func writeZip(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRead(t *testing.T, run execx.Runner, p string) string {
	t.Helper()
	s, err := Read(context.Background(), run, p)
	if err != nil {
		t.Fatalf("Read(%s): %v", filepath.Base(p), err)
	}
	return s
}

func TestReadRejectsBinary(t *testing.T) {
	cases := map[string][]byte{
		"blob.txt":  {0x89, 'P', 'N', 'G', 0, 0, 1, 2},
		"latin1.md": []byte("caf\xe9 cr\xe8me"),
		"data":      append([]byte("hello"), 0, 0, 0),
	}
	for name, data := range cases {
		_, err := Read(context.Background(), &fakeRun{}, writeFile(t, name, data))
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s: err=%v, muốn ErrUnsupported", name, err)
		}
	}
	for _, name := range []string{"sheet.xlsx", "deck.key", "a.zip"} {
		if _, err := Read(context.Background(), &fakeRun{}, writeFile(t, name, []byte("x"))); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	// docx giả (không phải zip) → ErrUnsupported, không đọc như text
	if _, err := Read(context.Background(), &fakeRun{}, writeFile(t, "fake.docx", []byte("plain"))); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("fake docx: %v", err)
	}
}

func TestReadPlainAndMarkdown(t *testing.T) {
	txt := mustRead(t, &fakeRun{}, writeFile(t, "a.txt", []byte("\xef\xbb\xbfxin chào\r\nthế giới")))
	if txt != "xin chào\nthế giới" {
		t.Fatalf("txt=%q", txt)
	}
	md := mustRead(t, &fakeRun{}, writeFile(t, "g.md", []byte("# Hướng dẫn\nmở đầu\n## Cài đặt\nbước 1\n```\n# không phải heading\n```\n### macOS\nbrew\n## Gỡ\nxoá")))
	for _, want := range []string{Marker("Hướng dẫn"), Marker("Hướng dẫn › Cài đặt"), Marker("Hướng dẫn › Cài đặt › macOS"), Marker("Hướng dẫn › Gỡ")} {
		if !strings.Contains(md, want) {
			t.Fatalf("thiếu %s trong:\n%s", want, md)
		}
	}
	if strings.Contains(md, Marker("không phải heading")) {
		t.Fatal("heading trong code fence bị tính")
	}
}

func TestReadPDFPagesViaRunner(t *testing.T) {
	f := &fakeRun{out: "trang một\f\fđoạn trang ba\f"}
	s := mustRead(t, f, writeFile(t, "a.pdf", []byte("%PDF-1.4")))
	if !strings.Contains(s, Marker("trang 1")+"\n\ntrang một") || !strings.Contains(s, Marker("trang 3")+"\n\nđoạn trang ba") {
		t.Fatalf("s=%q", s)
	}
	if strings.Contains(s, Marker("trang 2")) {
		t.Fatal("trang rỗng vẫn có marker")
	}
	if !strings.HasPrefix(f.calls[0], "pdftotext -enc UTF-8 ") {
		t.Fatalf("calls=%v", f.calls)
	}
	// PDF scan: không có chữ → lỗi rõ ràng
	_, err := Read(context.Background(), &fakeRun{out: "\f  \f"}, writeFile(t, "scan.pdf", []byte("%PDF")))
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "OCR") {
		t.Fatalf("scan err=%v", err)
	}
}

func TestReadDocx(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:t>Sổ tay</w:t></w:r></w:p>
<w:p><w:r><w:t>Mở </w:t></w:r><w:r><w:t xml:space="preserve">đầu.</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Chương &amp; một</w:t></w:r></w:p>
<w:p><w:r><w:t>Nội</w:t><w:tab/><w:t>dung</w:t><w:br/><w:t>dòng hai</w:t></w:r></w:p>
<w:p></w:p>
</w:body></w:document>`
	s := mustRead(t, &fakeRun{}, writeZip(t, "a.docx", map[string]string{"word/document.xml": doc, "[Content_Types].xml": "<x/>"}))
	want := Marker("Sổ tay") + "\n\nSổ tay\n\nMở đầu.\n\n" + Marker("Chương & một") + "\n\nChương & một\n\nNội\tdung\ndòng hai"
	if s != want {
		t.Fatalf("got:\n%q\nwant:\n%q", s, want)
	}
}

func TestReadPptxOrdersSlides(t *testing.T) {
	slide := func(lines ...string) string {
		var b strings.Builder
		b.WriteString(`<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree>`)
		for _, l := range lines {
			b.WriteString(`<p:sp><p:txBody><a:p><a:r><a:t>` + l + `</a:t></a:r></a:p></p:txBody></p:sp>`)
		}
		b.WriteString(`</p:spTree></p:cSld></p:sld>`)
		return b.String()
	}
	s := mustRead(t, &fakeRun{}, writeZip(t, "d.pptx", map[string]string{
		"ppt/slides/slide10.xml":            slide("Kết luận"),
		"ppt/slides/slide2.xml":             slide("Tiêu đề hai", "ý A"),
		"ppt/slides/slide1.xml":             slide("Mở đầu"),
		"ppt/slides/_rels/slide1.xml.rels":  "<r/>",
		"ppt/slideLayouts/slideLayout1.xml": slide("layout không đọc"),
	}))
	want := Marker("slide 1") + "\n\nMở đầu\n\n" + Marker("slide 2") + "\n\nTiêu đề hai\ný A\n\n" + Marker("slide 10") + "\n\nKết luận"
	if s != want {
		t.Fatalf("got:\n%q\nwant:\n%q", s, want)
	}
}

func TestReadEpubSpine(t *testing.T) {
	s := mustRead(t, &fakeRun{}, writeZip(t, "b.epub", map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`,
		"OEBPS/content.opf": `<package><manifest>
<item id="c2" href="text/ch%202.xhtml"/><item id="c1" href="text/ch1.xhtml"/><item id="css" href="s.css"/>
</manifest><spine><itemref idref="c1"/><itemref idref="c2"/><itemref idref="missing"/></spine></package>`,
		"OEBPS/text/ch1.xhtml":  `<html><head><title>x</title><style>p{}</style></head><body><h1>Chương Một</h1><p>Đoạn&nbsp;một <b>đậm</b>.</p><script>alert(1)</script></body></html>`,
		"OEBPS/text/ch 2.xhtml": `<html><body><p>Không có heading.<br/>Dòng hai</p></body></html>`,
	}))
	for _, want := range []string{Marker("Chương Một"), "Đoạn\u00a0một đậm.", Marker("chương 2"), "Không có heading.\nDòng hai"} {
		if !strings.Contains(s, want) {
			t.Fatalf("thiếu %q trong:\n%s", want, s)
		}
	}
	if strings.Contains(s, "alert") || strings.Contains(s, "p{}") {
		t.Fatalf("script/style lọt vào:\n%s", s)
	}
	if strings.Index(s, "Chương Một") > strings.Index(s, "Không có heading") {
		t.Fatal("sai thứ tự spine")
	}
}

func TestHTMLTolerant(t *testing.T) {
	s := HTMLText([]byte(`<!DOCTYPE html><html><body><nav>menu</nav><h2>Giới thiệu</h2><p>a &amp; b<p>đoạn hai<ul><li>một<li>hai</ul><pre>  code
  giữ</pre></body>`))
	for _, want := range []string{Marker("Giới thiệu"), "a & b", "đoạn hai", "một", "hai", "code"} {
		if !strings.Contains(s, want) {
			t.Fatalf("thiếu %q trong:\n%s", want, s)
		}
	}
	if strings.Contains(s, "menu") {
		t.Fatal("nav lọt vào")
	}
}

func TestMarkerClips(t *testing.T) {
	m := Marker(strings.Repeat("dài ", 50))
	if n := len([]rune(m)); n > 84 {
		t.Fatalf("marker %d rune", n)
	}
	if Marker("  a \n b ") != "⟦a b⟧" {
		t.Fatal(Marker("  a \n b "))
	}
}
