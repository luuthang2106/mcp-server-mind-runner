// Package e2e chạy kịch bản đầu-cuối trên binary thật (Task 7.5): build →
// setup non-interactive → MCP stdio qua SDK client → hooks → ingest
// text/media → maintenance — tất cả với gateway giả chạy trong process test
// (không mạng, không cần gateway thật). Chạy được cả ubuntu CI; nhánh video
// tự bỏ qua khi thiếu macOS/avconvert.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/server"
	"mind-runner/internal/store"
)

// Marker nhận diện từng đối tượng xuyên suốt các tầng (notes → FTS → recall).
const (
	markRemember   = "zorbophone"   // note ghi qua tool remember
	markExtract    = "kryptoscope"  // note do extract_session trích (chat giả)
	markTranscript = "chronohedron" // note transcript (omni giả)
	markIngestMD   = "nebelmarker"  // note nạp từ file .md
)

func TestFullE2E(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("không tìm thấy go trên PATH: %v", err)
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "mind-runner")
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/mind-runner")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// Gateway giả trong process test: /embeddings, /rerank, /chat/completions.
	// Chat (extract) trả JSON trích xuất; omni trả transcript giả.
	fake := egressfake.New(t, egressfake.Options{
		ChatResp: func(_, _ string) string {
			return fmt.Sprintf(
				`{"notes":[{"kind":"fact","text":"%s: dự án dùng SQLite vector cho trí nhớ dài hạn","tags":["e2e"]}],`+
					`"tasks":[{"title":"kiểm chứng chỉ số %s","next_step":"đối chiếu embeddings"}],"relations":[]}`,
				markExtract, markExtract)
		},
		OmniResp: func(_ []map[string]any) string {
			return "bản ghi: hôm nay bàn về " + markTranscript + " và vector store cho trí nhớ dài hạn"
		},
	})

	home := filepath.Join(tmp, "home")
	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// API key chỉ đi qua env (như setting MCP của client cấp cho server).
	env := append(os.Environ(), "HOME="+home, "MIND_RUNNER_CONFIG="+filepath.Join(home, "config.toml"),
		"MIND_RUNNER_GATEWAY_API_KEY=test-key")

	stdout, stderr, code := runCLI(t, bin, env, "", "setup", "--non-interactive", "--skip-launchd",
		"--data-dir", dataDir, "--gateway-url", fake.URL, "--gateway-key", "test-key",
		"--extract-model", "test-extract", "--omni-model", "test-omni")
	if code != 0 {
		t.Fatalf("setup exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// transcript của hook: JSONL nửa vời (dòng cuối đứt) — capture lưu raw
	// bytes, không parse, nên không được làm crash.
	tp := filepath.Join(tmp, "transcript.jsonl")
	content := `{"type":"user","message":"chốt dùng SQLite vector cho mind-runner"}` + "\n" +
		`{"type":"assistant","message":"đã ghi nhận quyết định"}` + "\n" + `{"type":"user","mess`
	if err := os.WriteFile(tp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// ── Phiên MCP 1: tools/list + write/read đường bộ nhớ ──────────────────
	cs1 := dialMCP(t, bin, env)

	tools, err := cs1.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"briefing", "export", "forget", "ingest", "recall",
		"remember", "task_add", "task_list", "task_update"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools/list = %v, muốn %v", names, want)
	}

	br := callTool[server.BriefingOut](t, cs1, "briefing", nil)
	if !br.Delivered {
		t.Fatalf("briefing đầu ngày phải delivered: %+v", br)
	}

	task := callTool[server.TaskAddOut](t, cs1, "task_add", map[string]any{
		"title": "hoàn thiện e2e mind-runner", "next_step": "chạy maintenance với gateway giả",
	})
	if task.TaskID <= 0 {
		t.Fatalf("task_add: %+v", task)
	}

	rem := callTool[server.RememberOut](t, cs1, "remember", map[string]any{
		"text": "Quyết định e2e: mã " + markRemember + " mở khoá phiên bản",
		"kind": "decision", "tags": []string{"e2e"},
	})
	if !rem.Created || rem.NoteID <= 0 {
		t.Fatalf("remember: %+v", rem)
	}

	// Recall ngay trong phiên — note chưa có embedding (job chờ maintenance),
	// tầng FTS + rerank phải vẫn tìm thấy.
	rec := callTool[server.RecallOut](t, cs1, "recall", map[string]any{"query": markRemember})
	if !anyHit(rec.Hits, markRemember) {
		t.Fatalf("recall %s: hits=%+v stages=%v", markRemember, rec.Hits, rec.Stages)
	}

	mdPath := filepath.Join(tmp, "ghi-chu-e2e.md")
	if err := os.WriteFile(mdPath, []byte("# Ghi chú e2e\n\nKiến trúc dùng "+markIngestMD+" để đánh dấu luồng ingest.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inText := callTool[server.IngestOut](t, cs1, "ingest", map[string]any{"path": mdPath})
	if inText.NoteID <= 0 || !inText.Created {
		t.Fatalf("ingest md: %+v", inText)
	}

	audioFixture, err := filepath.Abs("../internal/media/testdata/audio_short.m4a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(audioFixture); err != nil {
		t.Skipf("thiếu fixture audio: %v", err)
	}
	inAudio := callTool[server.IngestOut](t, cs1, "ingest", map[string]any{"path": audioFixture})
	if inAudio.MediaID <= 0 || inAudio.Status != "queued" || !inAudio.Created {
		t.Fatalf("ingest audio: %+v", inAudio)
	}
	if err := cs1.Close(); err != nil {
		t.Fatalf("đóng mcp phiên 1: %v", err)
	}

	// ── Hooks ──────────────────────────────────────────────────────────────
	// Briefing của ngày đã đốt qua tool MCP → session-start không in gì
	// (gate 1 lần/ngày); gọi lần thứ hai sau khi chốt phiên vẫn phải rỗng.
	evStart := `{"session_id":"sess-e2e","cwd":"` + tmp + `","transcript_path":"` + tp + `"}`
	hsOut, hsErr, code := runCLI(t, bin, env, evStart, "hook", "session-start")
	if code != 0 {
		t.Fatalf("hook session-start exit=%d stderr=%s", code, hsErr)
	}
	if hsOut != "" {
		t.Fatalf("hook session-start phải rỗng stdout (briefing đã đốt), được %q", hsOut)
	}

	evStop := `{"session_id":"sess-e2e","transcript_path":"` + tp + `"}`
	if _, stopErr, code := runCLI(t, bin, env, evStop, "hook", "stop"); code != 0 {
		t.Fatalf("hook stop exit=%d stderr=%s", code, stopErr)
	}
	// extract được gộp theo phiên (chờ ~10 phút); đóng phiên → chạy ngay
	if _, endErr, code := runCLI(t, bin, env, evStop, "hook", "session-end"); code != 0 {
		t.Fatalf("hook session-end exit=%d stderr=%s", code, endErr)
	}

	hs2Out, hs2Err, code := runCLI(t, bin, env, evStart, "hook", "session-start")
	if code != 0 {
		t.Fatalf("hook session-start #2 exit=%d stderr=%s", code, hs2Err)
	}
	if hs2Out != "" {
		t.Fatalf("hook session-start #2 phải rỗng, được %q", hs2Out)
	}

	// ── Maintenance: chạy trọn queue (embeddings + extract + transcribe) ──
	mOut, mErr, code := runCLI(t, bin, env, "", "maintenance")
	if code != 0 {
		t.Fatalf("maintenance exit=%d\nstdout:\n%s\nstderr:\n%s", code, mOut, mErr)
	}
	if fake.ChatCalls == 0 || fake.OmniCalls == 0 || fake.EmbedCalls == 0 || fake.RerankCalls == 0 {
		t.Fatalf("gateway giả chưa được dùng đủ: chat=%d omni=%d embed=%d rerank=%d",
			fake.ChatCalls, fake.OmniCalls, fake.EmbedCalls, fake.RerankCalls)
	}
	// log usage gateway: tổng hợp theo purpose, không chứa nội dung
	egLog, err := os.ReadFile(filepath.Join(dataDir, "logs", "egress.log"))
	if err != nil {
		t.Fatalf("thiếu logs/egress.log: %v", err)
	}
	for _, want := range []string{`"msg":"egress: tổng hợp"`, `"purpose":"extract_session"`, `"purpose":"recall"`} {
		if !strings.Contains(string(egLog), want) {
			t.Fatalf("egress.log thiếu %s:\n%s", want, egLog)
		}
	}
	if strings.Contains(string(egLog), "SQLite vector") {
		t.Fatal("egress.log chứa nội dung transcript")
	}

	// ── Assert DB sau maintenance ─────────────────────────────────────────
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return n
	}

	if n := count(`SELECT COUNT(*) FROM notes WHERE kind='decision' AND text LIKE '%` + markRemember + `%'`); n != 1 {
		t.Fatalf("note remember = %d, muốn 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM notes WHERE text LIKE '%` + markIngestMD + `%'`); n != 1 {
		t.Fatalf("note ingest md = %d, muốn 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM notes WHERE kind='fact' AND text LIKE '%` + markExtract + `%'`); n != 1 {
		t.Fatalf("note extract = %d, muốn 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM notes WHERE kind='transcript' AND text LIKE '%` + markTranscript + `%'`); n != 1 {
		t.Fatalf("note transcript = %d, muốn 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM session_raw`); n != 1 {
		t.Fatalf("session_raw = %d, muốn 1", n)
	}
	// Mọi chunk đều có embedding — kể cả chunk sinh ra trong lúc maintenance
	// đang chạy (job nối đuôi được claim tiếp trong cùng lượt Run).
	if n := count(`SELECT COUNT(*) FROM chunks c WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.chunk_id = c.id)`); n != 0 {
		t.Fatalf("chunk thiếu embedding = %d, muốn 0", n)
	}
	var status string
	var transcript, model *string
	if err := db.QueryRowContext(ctx,
		`SELECT status, transcript, model FROM media WHERE kind='audio'`).Scan(&status, &transcript, &model); err != nil {
		t.Fatal(err)
	}
	if status != "done" || transcript == nil || !strings.Contains(*transcript, markTranscript) ||
		model == nil || *model != "test-omni" {
		t.Fatalf("media audio: status=%s transcript=%v model=%v", status, transcript, model)
	}
	// Queue sạch: mọi job done, không job failed/dead/kẹt — trừ consolidate:
	// bước (3b) enqueue SAU queue (launchd không key), job nằm chờ sweep MCP.
	if n := count(`SELECT COUNT(*) FROM jobs WHERE state != 'done' AND type != 'consolidate'`); n != 0 {
		var jtype, state, lastErr string
		_ = db.QueryRowContext(ctx,
			`SELECT type, state, COALESCE(last_error,'') FROM jobs WHERE state != 'done' AND type != 'consolidate' LIMIT 1`).Scan(&jtype, &state, &lastErr)
		t.Fatalf("job chưa done: %d (vd %s/%s: %s)", n, jtype, state, lastErr)
	}
	if n := count(`SELECT COUNT(*) FROM jobs WHERE type='consolidate' AND state='queued'`); n != 1 {
		t.Fatalf("consolidate chờ sweep = %d, muốn đúng 1", n)
	}

	// ── Phiên MCP 2: recall cuối thấy transcript + note đã ingest ─────────
	cs2 := dialMCP(t, bin, env)

	recTr := callTool[server.RecallOut](t, cs2, "recall", map[string]any{"query": markTranscript})
	if !anyHit(recTr.Hits, markTranscript) {
		t.Fatalf("recall %s: hits=%+v stages=%v", markTranscript, recTr.Hits, recTr.Stages)
	}
	if !strings.HasPrefix(recTr.Stages["vector"], "ok:") {
		t.Fatalf("tầng vector phải chạy được sau khi embed: %v", recTr.Stages)
	}
	recMd := callTool[server.RecallOut](t, cs2, "recall", map[string]any{"query": markIngestMD})
	if !anyHit(recMd.Hits, markIngestMD) {
		t.Fatalf("recall %s: hits=%+v stages=%v", markIngestMD, recMd.Hits, recMd.Stages)
	}

	// ── Nhánh video (macOS + avconvert; CI ubuntu tự bỏ qua) ──────────────
	videoID := int64(0)
	switch {
	case runtime.GOOS != "darwin":
		t.Log("bỏ qua nhánh video: e2e video cần macOS + avconvert")
	default:
		if _, err := exec.LookPath("avconvert"); err != nil {
			t.Log("bỏ qua nhánh video: không có avconvert")
			break
		}
		videoFixture, err := filepath.Abs("../internal/media/testdata/video_short.mov")
		if err != nil {
			t.Fatal(err)
		}
		inVideo := callTool[server.IngestOut](t, cs2, "ingest", map[string]any{"path": videoFixture})
		if inVideo.MediaID <= 0 || inVideo.Status != "queued" {
			t.Fatalf("ingest video: %+v", inVideo)
		}
		videoID = inVideo.MediaID
	}
	if err := cs2.Close(); err != nil {
		t.Fatalf("đóng mcp phiên 2: %v", err)
	}

	if videoID > 0 {
		vOut, vErr, code := runCLI(t, bin, env, "", "maintenance")
		if code != 0 {
			t.Fatalf("maintenance #2 exit=%d\nstdout:\n%s\nstderr:\n%s", code, vOut, vErr)
		}
		var vStatus string
		var vTranscript *string
		if err := db.QueryRowContext(ctx,
			`SELECT status, transcript FROM media WHERE id=?`, videoID).Scan(&vStatus, &vTranscript); err != nil {
			t.Fatal(err)
		}
		if vStatus != "done" || vTranscript == nil || *vTranscript == "" {
			t.Fatalf("media video: status=%s transcript=%v", vStatus, vTranscript)
		}
	}
}

// dialMCP mở subprocess `mind-runner mcp` qua CommandTransport/stdio và trả
// session SDK; tự đóng khi test kết thúc (Close sớm vẫn an toàn).
func dialMCP(t *testing.T, bin string, env []string) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(bin, "mcp")
	cmd.Env = env
	var serverErr bytes.Buffer
	cmd.Stderr = &serverErr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).
		Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect mcp: %v\nstderr server:\n%s", err, serverErr.String())
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool gọi tool và unmarshal StructuredContent vào T; isError → fail kèm
// nội dung để đọc lý do thật từ handler.
func callTool[T any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s: isError: %v", name, res.Content)
	}
	var out T
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("call %s: remarshal %v: %v", name, res.StructuredContent, err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("call %s: parse structured %q: %v", name, data, err)
	}
	return out
}

// anyHit: có hit nào chứa substr trong text không.
func anyHit(hits []server.HitOut, substr string) bool {
	for _, h := range hits {
		if strings.Contains(h.Text, substr) {
			return true
		}
	}
	return false
}

// runCLI chạy một subcommand với env + stdin; trả stdout và stderr riêng
// (hook session-start phải rỗng stdout) kèm exit code.
func runCLI(t *testing.T, bin string, env []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("%v: %v", args, err)
		}
		return stdout.String(), stderr.String(), ee.ExitCode()
	}
	return stdout.String(), stderr.String(), 0
}
