package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

// ExportReport kết quả một lần export.
type ExportReport struct {
	Dir       string
	Notes     int
	Tasks     int
	Relations int
}

// Export ghi vault Markdown mở trực tiếp bằng Obsidian:
//
//	notes/<space>/<kind>/<id>-<slug>.md
//	tasks/<space>.md            (checklist theo status)
//	relations.md                (from → to (type) — note #id)
//
// Chỉ note chưa xoá mềm. outDir rỗng → data_dir/exports/<YYYYMMDD-HHMMSS>/.
func (b *Brain) Export(ctx context.Context, outDir string) (ExportReport, error) {
	if outDir == "" {
		outDir = filepath.Join(config.ExpandHome(b.cfg.DataDir), "exports", time.Now().Format("20060102-150405"))
	}
	sps, err := b.st.Spaces(ctx)
	if err != nil {
		return ExportReport{}, err
	}
	spaceName := make(map[int64]string, len(sps))
	for _, sp := range sps {
		spaceName[sp.ID] = sp.Name
	}
	name := func(id int64) string {
		if n, ok := spaceName[id]; ok {
			return n
		}
		return fmt.Sprintf("space-%d", id)
	}

	rep := ExportReport{Dir: outDir}

	notes, err := b.st.AllNotesForExport(ctx)
	if err != nil {
		return ExportReport{}, err
	}
	for _, n := range notes {
		rel := filepath.Join("notes", name(n.SpaceID), n.Kind, fmt.Sprintf("%d-%s.md", n.ID, slug(n.Text)))
		if err := writeFile(filepath.Join(outDir, rel), renderNote(n)); err != nil {
			return ExportReport{}, err
		}
	}
	rep.Notes = len(notes)

	tasks, err := b.st.AllTasksForExport(ctx)
	if err != nil {
		return ExportReport{}, err
	}
	// Gom theo space, giữ thứ tự xuất hiện đầu (AllTasksForExport sắp theo id).
	type taskGroup struct {
		space string
		lines []string
	}
	var groups []taskGroup
	bySpace := map[int64]int{}
	for _, t := range tasks {
		gi, ok := bySpace[t.SpaceID]
		if !ok {
			gi = len(groups)
			bySpace[t.SpaceID] = gi
			groups = append(groups, taskGroup{space: name(t.SpaceID)})
		}
		line := "- [" + taskBox(t.Status) + "] " + t.Title
		if t.NextStep != nil {
			line += " — bước kế: " + *t.NextStep
		}
		groups[gi].lines = append(groups[gi].lines, line)
	}
	for _, g := range groups {
		content := "# Việc — " + g.space + "\n\n" + strings.Join(g.lines, "\n") + "\n"
		if err := writeFile(filepath.Join(outDir, "tasks", g.space+".md"), content); err != nil {
			return ExportReport{}, err
		}
	}
	rep.Tasks = len(tasks)

	rels, err := b.st.AllRelationsForExport(ctx)
	if err != nil {
		return ExportReport{}, err
	}
	if len(rels) > 0 {
		var sb strings.Builder
		sb.WriteString("# Liên quan\n\n")
		for _, r := range rels {
			sb.WriteString("- " + r.FromRef + " → " + r.ToRef + " (" + r.RelType + ")")
			if r.SourceNoteID != nil {
				fmt.Fprintf(&sb, " — note #%d", *r.SourceNoteID)
			}
			sb.WriteString("\n")
		}
		if err := writeFile(filepath.Join(outDir, "relations.md"), sb.String()); err != nil {
			return ExportReport{}, err
		}
	}
	rep.Relations = len(rels)
	return rep, nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// renderNote: frontmatter YAML + body. tags theo JSON (mảng rỗng = []); source
// quote an toàn; session chỉ khi có.
func renderNote(n store.Note) string {
	tags := "[]"
	if len(n.Tags) > 0 {
		if data, err := json.Marshal(n.Tags); err == nil {
			tags = string(data)
		}
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("kind: " + n.Kind + "\n")
	sb.WriteString("tags: " + tags + "\n")
	sb.WriteString("created: " + n.CreatedAt.UTC().Format(time.RFC3339) + "\n")
	sb.WriteString("updated: " + n.UpdatedAt.UTC().Format(time.RFC3339) + "\n")
	sb.WriteString("source: " + strconv.Quote(n.Source) + "\n")
	if n.SessionID != nil {
		sb.WriteString("session: " + *n.SessionID + "\n")
	}
	sb.WriteString("---\n\n")
	sb.WriteString(n.Text + "\n")
	return sb.String()
}

func taskBox(status string) string {
	switch status {
	case "done":
		return "x"
	case "dropped":
		return "-"
	default:
		return " "
	}
}

var slugReplacer = strings.NewReplacer(
	" ", "-", "\t", "-", "\r", "-",
	"/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-",
)

// slug từ dòng đầu của text: lowercase, ký tự cấm → '-', giữ dấu tiếng Việt,
// tối đa 60 rune; rỗng → "note".
func slug(text string) string {
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	s := strings.Trim(slugReplacer.Replace(strings.ToLower(line)), "-")
	if r := []rune(s); len(r) > 60 {
		s = strings.Trim(string(r[:60]), "-")
	}
	if s == "" {
		return "note"
	}
	return s
}
