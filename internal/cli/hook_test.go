package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

func writeHookStdin(t *testing.T, s string) {
	t.Helper()
	old := stdin
	stdin = strings.NewReader(s)
	t.Cleanup(func() { stdin = old })
}

func TestHookStopEndToEnd(t *testing.T) {
	_, dataDir := setupFresh(t)
	ctx := context.Background()

	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := "{\"type\":\"user\"}\n{\"type\":\"assistant\"}\n"
	if err := os.WriteFile(tp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSessionStart(ctx, "sess-1", "claude-code", 1, &tp, time.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	writeHookStdin(t, `{"session_id":"sess-1","transcript_path":"`+tp+`"}`)
	var out, errb bytes.Buffer
	if code := RunHook([]string{"stop"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var nRaw int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_raw`).Scan(&nRaw); err != nil {
		t.Fatal(err)
	}
	if nRaw != 1 {
		t.Fatalf("session_raw=%d", nRaw)
	}
	sess, err := st.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.TranscriptOffset != int64(len(content)) {
		t.Fatalf("offset=%d, muốn %d", sess.TranscriptOffset, len(content))
	}
	counts, err := st.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["queued"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	var jobType string
	st.DB().QueryRowContext(ctx, `SELECT type FROM jobs`).Scan(&jobType)
	if jobType != "extract_session" {
		t.Fatalf("type=%s", jobType)
	}
}

// TestHookSessionStartEmitsBriefingOnce: hook session-start in briefing qua
// additionalContext đúng 1 lần/ngày; lần 2 cùng ngày không in gì, meta không đổi.
func TestHookSessionStartEmitsBriefingOnce(t *testing.T) {
	_, dataDir := setupFresh(t)
	ctx := context.Background()

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tasks(space_id, title, status, next_step, updated_at) VALUES(1,?,'open',NULL,?)`,
		"Chuẩn bị demo mind-runner", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertNote(ctx, &store.Note{
		SpaceID: 1, Kind: "decision", Text: "Dùng SQLite thay Postgres",
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	writeHookStdin(t, `{"session_id":"s1"}`)
	var out, errb bytes.Buffer
	if code := RunHook([]string{"session-start"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &payload); err != nil {
		t.Fatalf("stdout không phải 1 dòng JSON: %q err=%v", out.String(), err)
	}
	if payload.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("payload=%+v", payload)
	}
	c := payload.HookSpecificOutput.AdditionalContext
	if !strings.Contains(c, "Chuẩn bị demo mind-runner") || !strings.Contains(c, "Dùng SQLite thay Postgres") {
		t.Fatalf("briefing thiếu nội dung:\n%s", c)
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	before, ok, err := st.GetMeta(ctx, "last_briefing_date:1")
	if err != nil || !ok {
		t.Fatalf("meta=%q ok=%v err=%v", before, ok, err)
	}
	st.Close()

	// lần 2 cùng ngày → stdout rỗng, meta không đổi
	writeHookStdin(t, `{"session_id":"s2"}`)
	out.Reset()
	errb.Reset()
	if code := RunHook([]string{"session-start"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if out.String() != "" {
		t.Fatalf("lần 2 phải không in gì: %q", out.String())
	}
	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, _, err := st.GetMeta(ctx, "last_briefing_date:1")
	if err != nil || after != before {
		t.Fatalf("meta đổi: %q → %q err=%v", before, after, err)
	}
}

func TestHookGarbageStdinExitZero(t *testing.T) {
	_, dataDir := setupFresh(t)
	writeHookStdin(t, "không phải json{{{")
	var out, errb bytes.Buffer
	if code := RunHook([]string{"stop"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "logs", "hook.err.log")); err != nil {
		t.Fatalf("thiếu hook.err.log: %v", err)
	}
}

func TestHookUnknownSubcommand(t *testing.T) {
	_, _ = setupFresh(t)
	var out, errb bytes.Buffer
	if code := RunHook([]string{"gì-đó"}, &out, &errb, os.Getenv); code != 2 {
		t.Fatalf("exit=%d, muốn 2", code)
	}
}

func TestHookSessionStartResolvesSpace(t *testing.T) {
	cfgPath, dataDir := setupFresh(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spaces.Match.Rules = []config.MatchRule{{Glob: "~/work/**", Space: "work"}}
	if err := writeConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(os.Getenv("HOME"), "work", "proj")
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	writeHookStdin(t, fmt.Sprintf(`{"session_id":"s1","cwd":%q,"transcript_path":%q}`, cwd, tp))
	var out, errb bytes.Buffer
	if code := RunHook([]string{"session-start"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wantSpace, err := st.SpaceByName(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.SpaceID != wantSpace {
		t.Fatalf("space=%d, muốn %d", sess.SpaceID, wantSpace)
	}
	if sess.TranscriptPath == nil || *sess.TranscriptPath != tp {
		t.Fatalf("sess=%+v", sess)
	}
}

// fakeRunner ghi lại lệnh thay vì chạy thật.
type fakeRunner struct {
	calls [][]string
	err   error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, r.err
}

func TestSetupClaudeCodeWiring(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)

	old := runner
	fake := &fakeRunner{}
	runner = fake
	t.Cleanup(func() { runner = old })

	var out, errb bytes.Buffer
	dataDir := filepath.Join(tmp, "data")
	code := RunSetup([]string{"--non-interactive", "--gateway-key=k", "--skip-launchd", "--claude-code", "--data-dir", dataDir}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	// claude mcp add-json -s user mind-runner {...env key...}
	found := false
	want := "claude mcp add-json -s user mind-runner " + mcpServerJSON(mustExecutable(t), "k")
	for _, c := range fake.calls {
		if strings.Join(c, " ") == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls=%v", fake.calls)
	}

	// hooks đã merge
	data, err := os.ReadFile(filepath.Join(tmp, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hook session-start") || !strings.Contains(string(data), "hook prompt") {
		t.Fatalf("settings=%s", data)
	}

	// runner fail → warn, exit 0
	fake.err = fmt.Errorf("claude: command not found")
	out.Reset()
	errb.Reset()
	code = RunSetup([]string{"--non-interactive", "--gateway-key=k", "--skip-launchd", "--claude-code", "--data-dir", dataDir}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(out.String(), "chạy tay") {
		t.Fatalf("out=%s", out.String())
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestHookPromptRecapNewDay: session sống qua đêm (gập máy) — SessionStart
// không chạy lại, nhưng prompt đầu tiên của ngày mới nhận recap; prompt tiếp
// theo cùng ngày im lặng.
func TestHookPromptRecapNewDay(t *testing.T) {
	_, dataDir := setupFresh(t)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.InsertTask(ctx, 1, "Việc dang dở hôm qua", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	// hôm qua đã brief
	if err := st.SetMeta(ctx, "last_briefing_date:1", "2000-01-01"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out, errb bytes.Buffer
	writeHookStdin(t, `{"session_id":"overnight","prompt":"tiếp tục nhé"}`)
	if code := RunHook([]string{"prompt"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("stdout=%q err=%v", out.String(), err)
	}
	if payload.HookSpecificOutput.HookEventName != "UserPromptSubmit" ||
		!strings.Contains(payload.HookSpecificOutput.AdditionalContext, "Recap đầu ngày") ||
		!strings.Contains(payload.HookSpecificOutput.AdditionalContext, "Việc dang dở hôm qua") {
		t.Fatalf("payload=%+v", payload)
	}

	out.Reset()
	writeHookStdin(t, `{"session_id":"overnight","prompt":"câu tiếp"}`)
	if code := RunHook([]string{"prompt"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("prompt thứ 2 phải im lặng: %q", out.String())
	}
}

// TestSetupRerunKeepsKey: chạy lại setup --claude-code khi nâng cấp, không
// truyền --gateway-key → vẫn đăng ký MCP với key cũ (từ [gateway].api_key cũ
// trong config.toml, hoặc từ ~/.claude.json) thay vì đăng ký lại không key.
func TestSetupRerunKeepsKey(t *testing.T) {
	run := func(t *testing.T, tmp string) *fakeRunner {
		t.Helper()
		old := runner
		fake := &fakeRunner{}
		runner = fake
		t.Cleanup(func() { runner = old })
		var out, errb bytes.Buffer
		code := RunSetup([]string{"--non-interactive", "--skip-launchd", "--claude-code", "--data-dir", filepath.Join(tmp, "data")},
			&out, &errb, func(k string) string {
				if k == "MIND_RUNNER_GATEWAY_API_KEY" {
					return ""
				}
				return os.Getenv(k)
			})
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, errb.String())
		}
		return fake
	}
	hasKey := func(fake *fakeRunner, key string) bool {
		want := "claude mcp add-json -s user mind-runner " + mcpServerJSON(mustExecutable(t), key)
		for _, c := range fake.calls {
			if strings.Join(c, " ") == want {
				return true
			}
		}
		return false
	}

	t.Run("legacy config key", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		cfgPath := filepath.Join(tmp, "cfg", "config.toml")
		t.Setenv("MIND_RUNNER_CONFIG", cfgPath)
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte("[gateway]\napi_key = \"old-key\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := run(t, tmp)
		if !hasKey(fake, "old-key") {
			t.Fatalf("calls=%v", fake.calls)
		}
		data, _ := os.ReadFile(cfgPath)
		if strings.Contains(string(data), "old-key") {
			t.Fatalf("key cũ phải bị gỡ khỏi config: %s", data)
		}
	})

	t.Run("claude.json key", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		t.Setenv("MIND_RUNNER_CONFIG", filepath.Join(tmp, "cfg", "config.toml"))
		cj := `{"mcpServers":{"mind-runner":{"command":"x","args":["mcp"],"env":{"MIND_RUNNER_GATEWAY_API_KEY":"cc-key"}}}}`
		if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(cj), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := run(t, tmp)
		if !hasKey(fake, "cc-key") {
			t.Fatalf("calls=%v", fake.calls)
		}
	})
}
