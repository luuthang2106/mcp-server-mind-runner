package docread

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// DocxText: word/document.xml → đoạn văn; đoạn style Heading*/Title thành
// marker mục.
func DocxText(b []byte) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	var sb, para strings.Builder
	inPara, heading := false, false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: docx hỏng (%v)", ErrUnsupported, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				inPara, heading = true, false
				para.Reset()
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						v := strings.ToLower(a.Value)
						heading = strings.HasPrefix(v, "heading") || v == "title"
					}
				}
			case "tab":
				para.WriteByte('\t')
			case "br", "cr":
				para.WriteByte('\n')
			}
		case xml.CharData:
			if inPara {
				para.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inPara {
				inPara = false
				text := strings.TrimSpace(para.String())
				if text == "" {
					continue
				}
				if heading {
					sb.WriteString(Marker(text) + "\n\n")
				}
				sb.WriteString(text + "\n\n")
			}
		}
	}
	return sb.String(), nil
}

// SlideText: ppt/slides/slideN.xml → các đoạn <a:p>, mỗi đoạn một dòng.
func SlideText(b []byte) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	var sb, para strings.Builder
	inT := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: pptx hỏng (%v)", ErrUnsupported, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				para.Reset()
			case "t":
				inT = true
			case "br":
				para.WriteByte('\n')
			}
		case xml.CharData:
			if inT {
				para.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if s := strings.TrimSpace(para.String()); s != "" {
					sb.WriteString(s + "\n")
				}
			}
		}
	}
	return sb.String(), nil
}

var (
	htmlSkip  = map[string]bool{"script": true, "style": true, "head": true, "nav": true, "noscript": true, "svg": true, "template": true}
	htmlBlock = map[string]bool{"p": true, "div": true, "li": true, "tr": true, "section": true, "article": true,
		"blockquote": true, "pre": true, "h4": true, "h5": true, "h6": true, "dt": true, "dd": true, "table": true,
		"ul": true, "ol": true, "figure": true, "figcaption": true, "header": true, "footer": true, "main": true, "aside": true}
	htmlHeading = map[string]bool{"h1": true, "h2": true, "h3": true}
	blankLines  = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
	spaceRuns   = regexp.MustCompile(`[ \t\r\n\f]+`)
)

// HTMLText: HTML/XHTML → văn bản; h1–h3 thành marker mục; bỏ script/style/
// nav. Parser khoan dung: HTML lỗi thì trả phần đọc được.
func HTMLText(b []byte) string {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	var sb, head strings.Builder
	skip, inHead, pre := 0, false, 0
	write := func(s string) {
		if inHead {
			head.WriteString(s)
		} else {
			sb.WriteString(s)
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			switch {
			case htmlSkip[name]:
				skip++
			case skip > 0:
			case htmlHeading[name]:
				inHead = true
				head.Reset()
			case name == "br":
				write("\n")
			case htmlBlock[name]:
				write("\n\n")
				if name == "pre" {
					pre++
				}
			case name == "td" || name == "th":
				write("\t")
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			switch {
			case htmlSkip[name]:
				if skip > 0 {
					skip--
				}
			case skip > 0:
			case htmlHeading[name] && inHead:
				inHead = false
				if h := strings.TrimSpace(spaceRuns.ReplaceAllString(head.String(), " ")); h != "" {
					sb.WriteString("\n\n" + Marker(h) + "\n\n" + h + "\n\n")
				}
			case htmlBlock[name]:
				write("\n\n")
				if name == "pre" && pre > 0 {
					pre--
				}
			}
		case xml.CharData:
			if skip > 0 {
				continue
			}
			s := string(t)
			if pre == 0 {
				s = spaceRuns.ReplaceAllString(s, " ")
			}
			write(s)
		}
	}
	out := blankLines.ReplaceAllString(sb.String(), "\n\n")
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// EpubRootfile: đường dẫn file .opf trong META-INF/container.xml.
func EpubRootfile(container []byte) (string, error) {
	var c struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(container, &c); err != nil || len(c.Rootfiles) == 0 || c.Rootfiles[0].FullPath == "" {
		return "", fmt.Errorf("%w: epub thiếu rootfile", ErrUnsupported)
	}
	return c.Rootfiles[0].FullPath, nil
}

// EpubSpine: href các chương theo thứ tự đọc (spine) của file .opf.
func EpubSpine(opf []byte) ([]string, error) {
	var o struct {
		Items []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Refs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xml.Unmarshal(opf, &o); err != nil {
		return nil, fmt.Errorf("%w: epub .opf hỏng (%v)", ErrUnsupported, err)
	}
	byID := map[string]string{}
	for _, it := range o.Items {
		h := it.Href
		if u, err := url.PathUnescape(h); err == nil {
			h = u
		}
		byID[it.ID] = h
	}
	var out []string
	for _, r := range o.Refs {
		if h := byID[r.IDRef]; h != "" {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: epub không có chương nào trong spine", ErrUnsupported)
	}
	return out, nil
}

var mdHeading = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*#*\s*$`)

// MarkdownSections chèn marker "Mục cha › Mục con" trước mỗi heading #–###
// (bỏ qua trong code fence).
func MarkdownSections(s string) string {
	var sb strings.Builder
	var stack []string
	fence := ""
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if fence == "" && (strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~")) {
			fence = trim[:3]
		} else if fence != "" && strings.HasPrefix(trim, fence) {
			fence = ""
		} else if fence == "" {
			if m := mdHeading.FindStringSubmatch(line); m != nil {
				level := len(m[1])
				if len(stack) >= level {
					stack = stack[:level-1]
				}
				for len(stack) < level-1 {
					stack = append(stack, "")
				}
				stack = append(stack, m[2])
				var parts []string
				for _, p := range stack {
					if p != "" {
						parts = append(parts, p)
					}
				}
				sb.WriteString("\n" + Marker(strings.Join(parts, " › ")) + "\n\n")
			}
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}
