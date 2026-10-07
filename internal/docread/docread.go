// Package docread trích văn bản từ tài liệu (pdf, docx, pptx, epub, html,
// markdown, txt…) để nạp vào bộ nhớ. Kết quả chèn marker vị trí ⟦nhãn⟧ — một
// đoạn riêng (trang, slide, mục) — chunker gắn nhãn đó vào từng chunk để recall
// trích dẫn được "trang 42" thay vì chỉ tên file.
package docread

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"mind-runner/internal/execx"
)

// ErrUnsupported: định dạng chưa đọc được (file nhị phân, bảng tính, ảnh
// trong PDF scan…). Thông báo kèm theo nói rõ lý do.
var ErrUnsupported = errors.New("định dạng chưa hỗ trợ")

// maxZipEntry: trần giải nén mỗi file trong docx/pptx/epub (chống zip bomb).
const maxZipEntry = 64 << 20

// Marker: đoạn đánh dấu vị trí cho các mảnh văn bản phía sau.
func Marker(label string) string {
	label = strings.Join(strings.Fields(label), " ")
	if r := []rune(label); len(r) > 80 {
		label = string(r[:80]) + "…"
	}
	return "⟦" + label + "⟧"
}

// Read trả văn bản của file (UTF-8, có marker vị trí). Lỗi ErrUnsupported khi
// không phải văn bản đọc được — không bao giờ trả nội dung nhị phân.
func Read(ctx context.Context, run execx.Runner, p string) (string, error) {
	ext := strings.ToLower(filepath.Ext(p))
	var (
		text string
		err  error
	)
	switch ext {
	case ".pdf":
		text, err = readPDF(ctx, run, p)
	case ".docx":
		text, err = readDocx(p)
	case ".pptx":
		text, err = readPptx(p)
	case ".epub":
		text, err = readEpub(p)
	case ".html", ".htm", ".xhtml":
		var b []byte
		if b, err = os.ReadFile(p); err == nil {
			text = HTMLText(b)
		}
	case ".rtf", ".doc", ".odt", ".rtfd", ".webarchive":
		text, err = readTextutil(ctx, run, p)
	case ".xlsx", ".xls", ".numbers", ".pages", ".key", ".ppt", ".zip", ".gz", ".tar", ".7z", ".rar", ".dmg", ".exe", ".bin":
		return "", fmt.Errorf("%w: %s", ErrUnsupported, ext)
	default:
		text, err = readPlain(p)
		if err == nil && (ext == ".md" || ext == ".markdown" || ext == ".mdx") {
			text = MarkdownSections(text)
		}
	}
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if strings.TrimSpace(stripMarkers(text)) == "" {
		if ext == ".pdf" {
			return "", fmt.Errorf("%w: PDF không có lớp chữ (bản scan/ảnh) — chưa hỗ trợ OCR", ErrUnsupported)
		}
		return "", fmt.Errorf("file rỗng hoặc không có chữ: %s", p)
	}
	return text, nil
}

func stripMarkers(s string) string {
	var sb strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "⟦") && strings.HasSuffix(l, "⟧") {
			continue
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// readPlain: chỉ nhận văn bản UTF-8 — có byte NUL hoặc UTF-8 hỏng = nhị phân.
func readPlain(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("đọc file: %w", err)
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(b) {
		return "", fmt.Errorf("%w: %s là file nhị phân hoặc không phải UTF-8", ErrUnsupported, filepath.Base(p))
	}
	return string(b), nil
}

// readPDF: pdftotext (poppler, chất lượng tốt nhất) nếu có; không có thì trên
// macOS dùng PDFKit qua osascript (có sẵn). Trang tách bằng \f.
func readPDF(ctx context.Context, run execx.Runner, p string) (string, error) {
	out, err := run.Run(ctx, "pdftotext", "-enc", "UTF-8", p, "-")
	if err != nil && errors.Is(err, exec.ErrNotFound) && runtime.GOOS == "darwin" {
		out, err = run.Run(ctx, "osascript", "-l", "JavaScript", "-e", pdfKitJXA, p)
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: đọc PDF cần pdftotext (brew install poppler)", ErrUnsupported)
		}
		return "", fmt.Errorf("đọc PDF: %w", err)
	}
	return PDFPages(string(out)), nil
}

// pdfKitJXA: in text từng trang, ngăn bằng \f (giống pdftotext).
const pdfKitJXA = `ObjC.import('PDFKit');
function run(argv) {
  var d = $.PDFDocument.alloc.initWithURL($.NSURL.fileURLWithPath(argv[0]));
  if (!d || d.isNil()) throw new Error('không mở được PDF');
  var out = [];
  for (var i = 0; i < d.pageCount; i++) {
    var s = d.pageAtIndex(i).string;
    out.push(s.isNil() ? '' : s.js);
  }
  return out.join('\f');
}`

// PDFPages: text ngăn trang bằng \f → mỗi trang một marker "trang N" (bỏ trang rỗng).
func PDFPages(s string) string {
	var sb strings.Builder
	for i, page := range strings.Split(s, "\f") {
		page = strings.TrimSpace(page)
		if page == "" {
			continue
		}
		fmt.Fprintf(&sb, "%s\n\n%s\n\n", Marker("trang "+strconv.Itoa(i+1)), page)
	}
	return sb.String()
}

// readTextutil: rtf/doc/odt qua textutil có sẵn trên macOS.
func readTextutil(ctx context.Context, run execx.Runner, p string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("%w: %s chỉ đọc được trên macOS (textutil)", ErrUnsupported, filepath.Ext(p))
	}
	out, err := run.Run(ctx, "textutil", "-convert", "txt", "-stdout", p)
	if err != nil {
		return "", fmt.Errorf("textutil: %w", err)
	}
	if !utf8.Valid(out) {
		return "", fmt.Errorf("%w: textutil trả về không phải UTF-8", ErrUnsupported)
	}
	return string(out), nil
}

// zipFiles mở zip, trả map tên → *zip.File.
func zipFiles(p string) (*zip.ReadCloser, map[string]*zip.File, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: không mở được %s như file zip (%v)", ErrUnsupported, filepath.Base(p), err)
	}
	m := map[string]*zip.File{}
	for _, f := range zr.File {
		m[f.Name] = f
	}
	return zr, m, nil
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxZipEntry+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxZipEntry {
		return nil, fmt.Errorf("%w: %s quá lớn", ErrUnsupported, f.Name)
	}
	return b, nil
}

func readDocx(p string) (string, error) {
	zr, files, err := zipFiles(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	f := files["word/document.xml"]
	if f == nil {
		return "", fmt.Errorf("%w: docx thiếu word/document.xml", ErrUnsupported)
	}
	b, err := readZipEntry(f)
	if err != nil {
		return "", err
	}
	return DocxText(b)
}

func readPptx(p string) (string, error) {
	zr, files, err := zipFiles(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	type slide struct {
		n int
		f *zip.File
	}
	var slides []slide
	for name, f := range files {
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml"))
		if err == nil {
			slides = append(slides, slide{n, f})
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })
	var sb strings.Builder
	for _, s := range slides {
		b, err := readZipEntry(s.f)
		if err != nil {
			return "", err
		}
		t, err := SlideText(b)
		if err != nil {
			return "", err
		}
		if t = strings.TrimSpace(t); t != "" {
			fmt.Fprintf(&sb, "%s\n\n%s\n\n", Marker("slide "+strconv.Itoa(s.n)), t)
		}
	}
	return sb.String(), nil
}

func readEpub(p string) (string, error) {
	zr, files, err := zipFiles(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	get := func(name string) ([]byte, error) {
		f := files[name]
		if f == nil {
			return nil, fmt.Errorf("%w: epub thiếu %s", ErrUnsupported, name)
		}
		return readZipEntry(f)
	}
	container, err := get("META-INF/container.xml")
	if err != nil {
		return "", err
	}
	opfPath, err := EpubRootfile(container)
	if err != nil {
		return "", err
	}
	opf, err := get(opfPath)
	if err != nil {
		return "", err
	}
	hrefs, err := EpubSpine(opf)
	if err != nil {
		return "", err
	}
	base := path.Dir(opfPath)
	var sb strings.Builder
	chapter := 0
	for _, h := range hrefs {
		b, err := get(path.Clean(path.Join(base, h)))
		if err != nil {
			continue // mục spine trỏ file thiếu — bỏ qua, đọc tiếp
		}
		t := strings.TrimSpace(HTMLText(b))
		if stripMarkers(t) == "" {
			continue
		}
		chapter++
		if !strings.HasPrefix(t, "⟦") {
			t = Marker("chương "+strconv.Itoa(chapter)) + "\n\n" + t
		}
		sb.WriteString(t)
		sb.WriteString("\n\n")
	}
	return sb.String(), nil
}
