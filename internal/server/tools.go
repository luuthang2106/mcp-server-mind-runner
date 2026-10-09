package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mind-runner/internal/brain"
	"mind-runner/internal/capture"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
)

// bumpUsage đếm usage vào meta; lỗi counter chỉ log — không làm hỏng tool.
func bumpUsage(ctx context.Context, st *store.Store, key string) {
	if err := st.IncrMeta(ctx, key, 1); err != nil {
		slog.Default().Warn("incr meta", "key", key, "err", err.Error())
	}
}

// RememberIn — tham số tool remember; schema suy từ struct + jsonschema tags.
// Trường có cấu trúc đều tuỳ chọn: chỉ điền điều người dùng thực sự nói.
type RememberIn struct {
	Text string   `json:"text" jsonschema:"One self-contained sentence in the user's language (what happened / what is true). Keep names, numbers, dates. No template words."`
	Kind string   `json:"kind,omitempty" jsonschema:"decision (a choice made) | fact (stable truth about the user's world) | preference (how the user likes things) | procedure (a repeatable how-to) | note (event/observation, default) | task_hint (vague intention; use task_add for concrete to-dos)"`
	Tags []string `json:"tags,omitempty" jsonschema:"Short lowercase topic tags, e.g. project or person names"`
	Why  string   `json:"why,omitempty" jsonschema:"Reason or rationale, only if stated. Strongly recommended for decisions."`
	Who  []string `json:"who,omitempty" jsonschema:"People involved or deciding, only if stated"`
	When string   `json:"when,omitempty" jsonschema:"When it happened or applies (YYYY-MM-DD preferred), only if stated"`
	AsOf string   `json:"as_of,omitempty" jsonschema:"For facts that can change (prices, versions, headcount): date the fact was true (YYYY-MM-DD)"`
	Ref  string   `json:"ref,omitempty" jsonschema:"Source: URL, file path, document/meeting name, ticket id"`
	// decision
	Alternatives []string `json:"alternatives,omitempty" jsonschema:"Decision only: options considered and rejected"`
	Supersedes   int64    `json:"supersedes,omitempty" jsonschema:"note_id of an earlier decision/fact this one replaces (from recall); the old one is hidden from recall (soft delete)"`
	// preference
	Scope string `json:"scope,omitempty" jsonschema:"Preference only: where it applies, e.g. 'code reviews', 'emails to clients'"`
}

// RememberOut — kết quả ghi: created=false nghĩa là nội dung đã có (ghi lặp = ôn lại).
type RememberOut struct {
	NoteID  int64 `json:"note_id"`
	Created bool  `json:"created"`
	// Superseded: id note cũ đã XOÁ CỨNG (khi tool được gọi kèm supersedes).
	Superseded int64 `json:"superseded,omitempty"`
}

func registerRemember(srv *mcp.Server, b *brain.Brain, st *store.Store, defSpace string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "remember",
		Description: "Save something the user will want later: a decision (and why), a fact about their projects/people/setup, a preference, or a notable event. " +
			"Call it proactively the moment it comes up — do not wait for the end of the conversation or for the user to say 'remember'. " +
			"Fill only fields the user actually stated; never invent why/who/when. " +
			"If it replaces an earlier decision or fact, recall first and pass its note_id as supersedes (the old note is hidden from recall). " +
			"Do NOT use for: small talk, transient in-session details, things already in the code/repo, secret values (token, password, key — save the account and where its credential lives as kind=fact instead), or concrete to-dos (use task_add). " +
			"Accounts are worth a fact: which account for which service/project, where its credential lives (Keychain item, 1Password, env var), expiry. To keep a secret, offer Keychain (security add-generic-password -s <service> -a <account> -w — typed at its prompt, never pasted into chat); never echo a secret value. " +
			"Idempotent: saving identical text again only refreshes it (created=false).",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RememberIn) (*mcp.CallToolResult, RememberOut, error) {
		kind := strings.ToLower(strings.TrimSpace(in.Kind))
		if kind == "" {
			kind = "note"
		}
		switch kind {
		case "note", "fact", "preference", "decision", "task_hint", "procedure":
		default:
			return nil, RememberOut{}, fmt.Errorf("invalid kind %q (note|fact|preference|decision|task_hint|procedure)", in.Kind)
		}
		for _, d := range []string{in.When, in.AsOf} {
			if err := checkDateish(d); err != nil {
				return nil, RememberOut{}, err
			}
		}
		spaceID, err := st.SpaceByName(ctx, defSpace)
		if err != nil {
			return nil, RememberOut{}, err
		}
		if in.Supersedes != 0 {
			old, err := st.FetchNote(ctx, in.Supersedes)
			if err != nil || old.DeletedAt != nil {
				return nil, RememberOut{}, fmt.Errorf("supersedes: note %d not found", in.Supersedes)
			}
			if old.SpaceID != spaceID {
				return nil, RememberOut{}, fmt.Errorf("supersedes: note %d is in another space", in.Supersedes)
			}
		}
		sess, err := capture.ResolveSession(ctx, st, capture.ClientDesktop, spaceID, time.Now())
		if err != nil {
			return nil, RememberOut{}, err
		}
		res, err := b.WriteNote(ctx, brain.WriteParams{
			SpaceID:   spaceID,
			Kind:      kind,
			Text:      in.Text,
			Tags:      normTags(in.Tags),
			Source:    "tool:remember",
			SessionID: &sess,
			Meta: store.NoteMeta{
				Why: strings.TrimSpace(in.Why), Who: trimAll(in.Who), When: strings.TrimSpace(in.When),
				AsOf: strings.TrimSpace(in.AsOf), Ref: strings.TrimSpace(in.Ref),
				Alternatives: trimAll(in.Alternatives), Scope: strings.TrimSpace(in.Scope),
			},
		})
		if err != nil {
			return nil, RememberOut{}, err
		}
		out := RememberOut{NoteID: res.NoteID, Created: res.Fresh}
		if in.Supersedes != 0 && in.Supersedes != res.NoteID {
			// xoá mềm: ẩn khỏi recall ngay, purge xoá thật sau retention.jobs_days
			// — chọn nhầm id vẫn cứu được (ghi lại đúng nội dung cũ là hồi sinh).
			if _, err := st.SoftDeleteNote(ctx, in.Supersedes, time.Now()); err != nil {
				return nil, RememberOut{}, err
			}
			out.Superseded = in.Supersedes
		}
		bumpUsage(ctx, st, "stats.remember_calls")
		return nil, out, nil
	})
}

// RecallIn — tham số tool recall; schema suy từ struct + jsonschema tags.
type RecallIn struct {
	Query   string   `json:"query" jsonschema:"Specific keywords: names, project, document title, topic. Vietnamese without diacritics also matches."`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"Restrict to kinds: decision, fact, preference, procedure, note, task_hint, document (ingested files), transcript (audio/video), caption (images). Omit to search everything."`
	Tags    []string `json:"tags,omitempty" jsonschema:"Only notes carrying all these tags (ingested files are tagged file:<name>)"`
	Project string   `json:"project,omitempty" jsonschema:"Only this project (git repo name). Omit — results from the current project are already ranked first; set only when the user asks about a specific other project."`
	Limit   int      `json:"limit,omitempty" jsonschema:"Max results (default 5)"`
}

// HitOut một kết quả recall cho client — đủ nguồn/ngày để trích dẫn và tự
// đánh giá độ liên quan.
type HitOut struct {
	NoteID       int64    `json:"note_id"`
	Text         string   `json:"text"`
	Kind         string   `json:"kind"`
	Project      string   `json:"project,omitempty"`
	Source       string   `json:"source"`
	Title        string   `json:"title,omitempty"`
	Why          string   `json:"why,omitempty"`
	Who          []string `json:"who,omitempty"`
	When         string   `json:"when,omitempty"`
	AsOf         string   `json:"as_of,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Score        float64  `json:"score"`
}

// RecallOut — hits kèm stages: tầng nào chạy/suy giảm đều hiện rõ.
type RecallOut struct {
	Hits   []HitOut          `json:"hits"`
	Stages map[string]string `json:"stages"`
}

func registerRecall(srv *mcp.Server, b *brain.Brain, st *store.Store, defSpace string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "recall",
		Description: "Search the user's long-term memory: decisions, facts, preferences, past sessions, and the content of documents, audio transcripts and image captions they ingested. " +
			"Call it before answering whenever the answer may depend on what the user saved or did before: references to the past ('last time', 'what did we decide'), " +
			"their documents/meetings/recordings, or their own projects, people and terms you are unsure about. " +
			"Use kinds to narrow (e.g. [\"document\"] for questions about a file, [\"decision\"] for 'why did we choose X'). " +
			"Cite source/when/ref from hits. Empty hits = not saved: say so, never make it up. " +
			"Do NOT use for general knowledge or code that is open in the current repo. " +
			"Check stages for degraded layers (\"error: …\").",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RecallIn) (*mcp.CallToolResult, RecallOut, error) {
		var kinds []string
		for _, k := range in.Kinds {
			k = strings.ToLower(strings.TrimSpace(k))
			if !brain.ValidKind(k) {
				return nil, RecallOut{}, fmt.Errorf("invalid kind %q (decision|fact|preference|procedure|note|task_hint|document|transcript|caption)", k)
			}
			kinds = append(kinds, k)
		}
		res, err := b.Recall(ctx, brain.RecallParams{
			Query:   in.Query,
			Project: strings.TrimSpace(in.Project),
			Tags:    normTags(in.Tags),
			Kinds:   kinds,
			Limit:   in.Limit,
		})
		if err != nil {
			return nil, RecallOut{}, err
		}
		bumpUsage(ctx, st, "stats.recall_calls")
		out := RecallOut{Hits: make([]HitOut, len(res.Hits)), Stages: res.Stages}
		for i, h := range res.Hits {
			ho := HitOut{
				NoteID:       h.NoteID,
				Text:         h.Text,
				Kind:         h.Kind,
				Project:      h.Project,
				Source:       h.Source,
				Title:        h.Meta.Title,
				Why:          h.Meta.Why,
				Who:          h.Meta.Who,
				When:         h.Meta.When,
				AsOf:         h.Meta.AsOf,
				Ref:          h.Meta.Ref,
				Alternatives: h.Meta.Alternatives,
				Scope:        h.Meta.Scope,
				Tags:         h.Tags,
				CreatedAt:    h.CreatedAt.UTC().Format(time.RFC3339),
				UpdatedAt:    h.UpdatedAt.UTC().Format(time.RFC3339),
				Score:        h.Score,
			}
			if h.SessionID != nil {
				ho.SessionID = *h.SessionID
			}
			out.Hits[i] = ho
		}
		return nil, out, nil
	})
}

// TaskAddIn — tham số tool task_add (5W: what=title, why, who=owner/waiting_on,
// when=due, how=constraints/next_step). Mọi trường ngoài title tuỳ chọn.
type TaskAddIn struct {
	Title       string `json:"title" jsonschema:"What: short actionable title starting with a verb"`
	Why         string `json:"why,omitempty" jsonschema:"Why: purpose or expected outcome, only if stated"`
	Owner       string `json:"owner,omitempty" jsonschema:"Who does it, if not the user"`
	WaitingOn   string `json:"waiting_on,omitempty" jsonschema:"Person/thing this is blocked on, if any"`
	Due         string `json:"due,omitempty" jsonschema:"Deadline YYYY-MM-DD (resolve 'Friday', 'next week' against today's date)"`
	Constraints string `json:"constraints,omitempty" jsonschema:"How: constraints, acceptance criteria, approach"`
	NextStep    string `json:"next_step,omitempty" jsonschema:"The very next concrete action, if known"`
}

// TaskAddOut — task_id vừa tạo (hoặc task open trùng title đã có).
type TaskAddOut struct {
	TaskID  int64 `json:"task_id"`
	Created bool  `json:"created"`
}

func registerTaskAdd(srv *mcp.Server, b *brain.Brain, st *store.Store, defSpace string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "task_add",
		Description: "Track a concrete open loop: something the user (or someone they wait on) still has to do. " +
			"Call it proactively when the user commits to, is assigned, or leaves unfinished a specific action. " +
			"Capture due date, owner/waiting_on, why and next_step only when stated. " +
			"Do NOT use for vague wishes (remember kind=task_hint) or steps you are about to do yourself in this session. " +
			"An open task with the same title in the same project is reused and its empty fields filled (created=false).",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in TaskAddIn) (*mcp.CallToolResult, TaskAddOut, error) {
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return nil, TaskAddOut{}, fmt.Errorf("title is empty")
		}
		if err := checkDate(in.Due); err != nil {
			return nil, TaskAddOut{}, err
		}
		spaceID, err := st.SpaceByName(ctx, defSpace)
		if err != nil {
			return nil, TaskAddOut{}, err
		}
		id, created, err := st.InsertTaskWith(ctx, spaceID, title, store.TaskFields{
			NextStep: optStr(in.NextStep), Why: optStr(in.Why), Owner: optStr(in.Owner),
			WaitingOn: optStr(in.WaitingOn), DueAt: optStr(in.Due), Constraints: optStr(in.Constraints),
			Project: b.Project(),
		}, time.Now())
		if err != nil {
			return nil, TaskAddOut{}, err
		}
		return nil, TaskAddOut{TaskID: id, Created: created}, nil
	})
}

// TaskUpdateIn — tham số tool task_update. Chuỗi rỗng/bỏ trống = giữ nguyên;
// "-" = xoá trường đó.
type TaskUpdateIn struct {
	TaskID      int64  `json:"task_id" jsonschema:"Task id (from task_list or briefing)"`
	Status      string `json:"status,omitempty" jsonschema:"open | done | dropped"`
	NextStep    string `json:"next_step,omitempty" jsonschema:"New next action"`
	Why         string `json:"why,omitempty"`
	Owner       string `json:"owner,omitempty"`
	WaitingOn   string `json:"waiting_on,omitempty" jsonschema:"Blocked on whom/what; '-' clears (no longer waiting)"`
	Due         string `json:"due,omitempty" jsonschema:"New deadline YYYY-MM-DD; '-' clears"`
	Constraints string `json:"constraints,omitempty"`
}

// TaskUpdateOut — updated=false khi không có field nào để đổi.
type TaskUpdateOut struct {
	Updated bool   `json:"updated"`
	Status  string `json:"status"`
}

func registerTaskUpdate(srv *mcp.Server, st *store.Store) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "task_update",
		Description: "Update a tracked task when the user reports progress: done, dropped, a new next step, a new deadline, or it is now blocked/unblocked. " +
			"Call it proactively when the conversation shows a known task changed — find the id with task_list if needed. " +
			"Empty fields are kept; '-' clears a field.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in TaskUpdateIn) (*mcp.CallToolResult, TaskUpdateOut, error) {
		switch in.Status {
		case "", "open", "done", "dropped":
		default:
			return nil, TaskUpdateOut{}, fmt.Errorf("invalid status %q (open|done|dropped)", in.Status)
		}
		if in.Due != "-" {
			if err := checkDate(in.Due); err != nil {
				return nil, TaskUpdateOut{}, err
			}
		}
		var status *string
		if in.Status != "" {
			status = &in.Status
		}
		f := store.TaskFields{
			NextStep: updStr(in.NextStep), Why: updStr(in.Why), Owner: updStr(in.Owner),
			WaitingOn: updStr(in.WaitingOn), DueAt: updStr(in.Due), Constraints: updStr(in.Constraints),
		}
		task, err := st.UpdateTaskWith(ctx, in.TaskID, status, f, time.Now(), false)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, TaskUpdateOut{}, fmt.Errorf("task %d not found", in.TaskID)
			}
			return nil, TaskUpdateOut{}, err
		}
		changed := status != nil || f != (store.TaskFields{})
		return nil, TaskUpdateOut{Updated: changed, Status: task.Status}, nil
	})
}

// TaskListIn — tham số tool task_list.
type TaskListIn struct {
	Status  string `json:"status,omitempty" jsonschema:"open (default) | done | dropped"`
	Project string `json:"project,omitempty" jsonschema:"Only this project (git repo name). Omit to list everything — the current project comes first."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Max tasks (default 20)"`
}

// TaskItem một dòng task cho client; stale = open quá 14 ngày không đụng.
type TaskItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	NextStep    string `json:"next_step,omitempty"`
	Why         string `json:"why,omitempty"`
	Owner       string `json:"owner,omitempty"`
	WaitingOn   string `json:"waiting_on,omitempty"`
	Due         string `json:"due,omitempty"`
	Overdue     bool   `json:"overdue,omitempty"`
	Constraints string `json:"constraints,omitempty"`
	Project     string `json:"project,omitempty"`
	Stale       bool   `json:"stale"`
	UpdatedAt   string `json:"updated_at"`
}

// TaskListOut — danh sách task.
type TaskListOut struct {
	Tasks []TaskItem `json:"tasks"`
}

func registerTaskList(srv *mcp.Server, b *brain.Brain, st *store.Store, defSpace string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "task_list",
		Description: "List the user's tracked tasks with due dates, owners and blockers. " +
			"Use when they ask what is pending/overdue, or to find a task id before task_update. " +
			"stale=true means untouched for 14+ days — ask whether it is still relevant.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in TaskListIn) (*mcp.CallToolResult, TaskListOut, error) {
		spaceID, err := st.SpaceByName(ctx, defSpace)
		if err != nil {
			return nil, TaskListOut{}, err
		}
		status := in.Status
		if status == "" {
			status = "open"
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		if status == "all" {
			status = ""
		}
		tasks, err := st.QueryTasks(ctx, spaceID, store.TaskQuery{
			Status: status, Project: strings.TrimSpace(in.Project), Prefer: b.Project(), Limit: limit})
		if err != nil {
			return nil, TaskListOut{}, err
		}
		now := nowFunc()
		today := now.Format("2006-01-02")
		out := TaskListOut{Tasks: make([]TaskItem, 0, len(tasks))}
		for _, t := range tasks {
			item := TaskItem{
				ID: t.ID, Title: t.Title, Status: t.Status,
				Why: t.Why, Owner: t.Owner, WaitingOn: t.WaitingOn, Due: t.DueAt, Constraints: t.Constraints,
				Project:   t.Project,
				Overdue:   t.Status == "open" && t.DueAt != "" && t.DueAt < today,
				Stale:     t.Status == "open" && now.Sub(t.UpdatedAt) > brain.StaleTaskAfter,
				UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
			}
			if t.NextStep != nil {
				item.NextStep = *t.NextStep
			}
			out.Tasks = append(out.Tasks, item)
		}
		return nil, out, nil
	})
}

// optStr: "" → nil (không đặt trường).
func optStr(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

// updStr: "" → nil (giữ nguyên); "-" → "" (xoá); còn lại → giá trị.
func updStr(v string) *string {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return nil
	case "-":
		empty := ""
		return &empty
	}
	return &v
}

// checkDate: rỗng hoặc YYYY-MM-DD hợp lệ.
func checkDate(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return fmt.Errorf("date %q must be YYYY-MM-DD", v)
	}
	return nil
}

// checkDateish: when/as_of cho phép YYYY, YYYY-MM, YYYY-MM-DD hoặc mô tả
// tự do có chữ (vd "Q3 2025"); chỉ chặn chuỗi trông như ngày nhưng sai định
// dạng (vd "12/03/2025" — mơ hồ ngày/tháng).
func checkDateish(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if _, err := time.Parse(layout, v); err == nil {
			return nil
		}
	}
	if strings.ContainsAny(v, "/") && strings.Trim(v, "0123456789/ ") == "" {
		return fmt.Errorf("ambiguous date %q: use YYYY-MM-DD", v)
	}
	return nil
}

// normTags: trim, bỏ rỗng/trùng. KHÔNG hạ chữ thường: tag cũ có thể viết hoa,
// lọc recall so khớp chính xác.
func normTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// trimAll: trim từng phần tử, bỏ rỗng.
func trimAll(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// IngestIn — tham số tool ingest.
type IngestIn struct {
	Path    string   `json:"path" jsonschema:"Absolute path of a document (pdf, docx, pptx, epub, html, md, txt; rtf/doc/odt on macOS) or media file (audio, image, video)"`
	Tags    []string `json:"tags,omitempty" jsonschema:"Topic tags (project, client, meeting series); file:<name> is added automatically"`
	Title   string   `json:"title,omitempty" jsonschema:"Human title of the document (default: file name)"`
	Summary string   `json:"summary,omitempty" jsonschema:"Optional 1-2 sentence summary, only if you have read the file"`
	Kind    string   `json:"kind,omitempty" jsonschema:"Text files only: document (default) | note | fact | preference | decision | task_hint"`
}

// IngestOut — text: note_id + created=false khi nội dung đã có; media:
// media_id + status (transcribe/caption chạy nền theo queue).
type IngestOut struct {
	NoteID  int64  `json:"note_id,omitempty"`
	Created bool   `json:"created"`
	MediaID int64  `json:"media_id,omitempty"`
	Status  string `json:"status,omitempty"`
}

func registerIngest(srv *mcp.Server, b *brain.Brain, st *store.Store, md *media.Media, defSpace string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "ingest",
		Description: "Store a file in long-term memory so it can be recalled later: documents (pdf, docx, pptx, epub, html, markdown, text) are split into chunks labelled with page/slide/section so recall can cite \"page 42\", audio/video transcribed, images captioned. " +
			"Scanned PDFs without a text layer, spreadsheets and binaries are rejected — tell the user rather than retrying. " +
			"Use ONLY when the user asks to save/ingest/import a file — never on your own initiative; recordings that include other people need their consent. " +
			"Add topic tags so later recall can filter. " +
			"Idempotent (text by content, media by file sha256). Media returns immediately; transcription/captioning runs in the background.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in IngestIn) (*mcp.CallToolResult, IngestOut, error) {
		spaceID, err := st.SpaceIDOrList(ctx, defSpace)
		if err != nil {
			return nil, IngestOut{}, err
		}
		// ingest là một trong ba event mở/touch session Desktop (spec §5).
		if _, err := capture.ResolveSession(ctx, st, capture.ClientDesktop, spaceID, time.Now()); err != nil {
			return nil, IngestOut{}, err
		}
		if media.IsMedia(in.Path) {
			res, err := md.Ingest(ctx, in.Path, spaceID, "tool:ingest")
			if err != nil {
				return nil, IngestOut{}, err
			}
			return nil, IngestOut{MediaID: res.MediaID, Status: res.Status, Created: res.Created}, nil
		}
		res, err := b.IngestText(ctx, brain.IngestTextParams{
			Path: in.Path, SpaceID: spaceID, Kind: strings.ToLower(strings.TrimSpace(in.Kind)),
			Tags: normTags(in.Tags), Title: in.Title, Summary: in.Summary,
		})
		if err != nil {
			return nil, IngestOut{}, err
		}
		return nil, IngestOut{NoteID: res.NoteID, Created: res.Fresh}, nil
	})
}

// nowFunc tách riêng để test bơm mốc thời gian cho tool briefing.
var nowFunc = time.Now

// BriefingIn — tham số tool briefing.
type BriefingIn struct {
	Force bool `json:"force,omitempty" jsonschema:"Bypass the once-per-day gate — only when the user explicitly asks for a recap"`
}

// BriefingOut — delivered=false kèm reason khi gate chặn (hôm nay đã briefing).
type BriefingOut struct {
	Delivered bool   `json:"delivered"`
	Reason    string `json:"reason,omitempty"`
	Text      string `json:"text"`
}

func registerBriefing(srv *mcp.Server, b *brain.Brain, st *store.Store, defSpace string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "briefing",
		Description: "Load the user's context at the start of a conversation: overdue/due tasks, blocked tasks, open tasks, recent decisions and preferences, recent sessions. " +
			"Call at the start of every new conversation, before answering the first message. Gated to once per day (day starts at [briefing].day_start_hour, default 04:00): " +
			"delivered=false means today's recap was already shown in another conversation — do not retry. Use force=true only when the user asks for a recap.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in BriefingIn) (*mcp.CallToolResult, BriefingOut, error) {
		spaceID, err := st.SpaceByName(ctx, defSpace)
		if err != nil {
			return nil, BriefingOut{}, err
		}
		now := nowFunc()
		// Briefing là event đầu phổ biến nhất của Desktop — mở/touch session (heuristic 30') trước khi assemble.
		if _, err := capture.ResolveSession(ctx, st, capture.ClientDesktop, spaceID, now); err != nil {
			return nil, BriefingOut{}, err
		}
		br, err := b.Briefing(ctx, brain.BriefingParams{SpaceID: spaceID, Force: in.Force, Now: now})
		if err != nil {
			return nil, BriefingOut{}, err
		}
		return nil, BriefingOut{Delivered: br.Delivered, Reason: br.Reason, Text: br.Text}, nil
	})
}

// ExportIn — tham số tool export.
type ExportIn struct {
	Dir string `json:"dir,omitempty" jsonschema:"Target directory (default: <data_dir>/exports/<timestamp>)"`
}

// ExportOut — khớp ExportReport; dir là thư mục thực ghi.
type ExportOut struct {
	Dir       string `json:"dir"`
	Notes     int    `json:"notes"`
	Tasks     int    `json:"tasks"`
	Relations int    `json:"relations"`
}

func registerExport(srv *mcp.Server, b *brain.Brain) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "export",
		Description: "Export the whole memory as a Markdown vault (opens in Obsidian): notes by space/kind with metadata, tasks, relations. " +
			"Use only when the user asks for an export/backup. Overwrites files in the target directory.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExportIn) (*mcp.CallToolResult, ExportOut, error) {
		rep, err := b.Export(ctx, in.Dir)
		if err != nil {
			return nil, ExportOut{}, err
		}
		return nil, ExportOut{Dir: rep.Dir, Notes: rep.Notes, Tasks: rep.Tasks, Relations: rep.Relations}, nil
	})
}

// ForgetIn — tham số tool forget.
type ForgetIn struct {
	Kind string `json:"kind" jsonschema:"note | task"`
	ID   int64  `json:"id" jsonschema:"note_id (from recall) or task id (from task_list)"`
}

// ForgetOut — forgotten=false kèm reason khi không có gì để quên (không isError).
type ForgetOut struct {
	Forgotten bool   `json:"forgotten"`
	Hint      string `json:"hint,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const forgetHint = "removed from recall/briefing now; data is physically deleted by the next maintenance purge; saving the identical text again revives it"

func boolPtr(v bool) *bool { return &v }

func registerForget(srv *mcp.Server, st *store.Store) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "forget",
		Description: "Forget a note or task (soft delete — disappears from recall/briefing immediately). " +
			"Use only when the user asks to forget/delete something: recall or task_list first, show what will be removed and confirm before calling. " +
			"For a decision that merely changed, use remember with supersedes instead (the old note is hidden from recall).",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ForgetIn) (*mcp.CallToolResult, ForgetOut, error) {
		now := time.Now()
		switch in.Kind {
		case "note":
			ok, err := st.SoftDeleteNote(ctx, in.ID, now)
			if err != nil {
				return nil, ForgetOut{}, err
			}
			if !ok {
				return nil, ForgetOut{Reason: "note not found (or already forgotten)"}, nil
			}
			return nil, ForgetOut{Forgotten: true, Hint: forgetHint}, nil
		case "task":
			ok, err := st.DropTask(ctx, in.ID, now)
			if err != nil {
				return nil, ForgetOut{}, err
			}
			if !ok {
				return nil, ForgetOut{Reason: "task not found (or already dropped)"}, nil
			}
			return nil, ForgetOut{Forgotten: true, Hint: forgetHint}, nil
		default:
			return nil, ForgetOut{}, fmt.Errorf("invalid kind %q (note|task)", in.Kind)
		}
	})
}
