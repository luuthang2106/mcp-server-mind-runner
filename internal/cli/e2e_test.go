package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/store"
)

// TestE2EHookStop build binary thật, setup trong HOME tạm, rồi chạy
// hook session-start + hook stop qua process với transcript JSONL nửa vời
// (dòng JSON đứt không được làm crash — raw chỉ là bytes).
func TestE2EHookStop(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
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

	home := filepath.Join(tmp, "home")
	dataDir := filepath.Join(home, "data")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+home, "MIND_RUNNER_CONFIG="+filepath.Join(home, "config.toml"))

	setup := exec.Command(bin, "setup", "--non-interactive", "--gateway-key", "test-key", "--skip-launchd", "--data-dir", dataDir)
	setup.Env = env
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}

	// transcript nửa vời: 2 dòng hợp lệ + 1 dòng JSON đứt
	content := "{\"a\":1}\n{\"b\":2}\n{\"c\":\n"
	tp := filepath.Join(tmp, "transcript.jsonl")
	if err := os.WriteFile(tp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	start := exec.Command(bin, "hook", "session-start")
	start.Env = env
	start.Stdin = strings.NewReader(`{"session_id":"sess-e2e","cwd":"` + tmp + `","transcript_path":"` + tp + `"}`)
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("session-start: %v\n%s", err, out)
	}

	stop := exec.Command(bin, "hook", "stop")
	stop.Env = env
	stop.Stdin = strings.NewReader(`{"session_id":"sess-e2e","transcript_path":"` + tp + `"}`)
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("hook stop: %v\n%s", err, out)
	}

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var nRaw int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_raw`).Scan(&nRaw); err != nil {
		t.Fatal(err)
	}
	if nRaw != 1 {
		t.Fatalf("session_raw=%d", nRaw)
	}
	var jobType, state string
	if err := st.DB().QueryRowContext(ctx, `SELECT type, state FROM jobs`).Scan(&jobType, &state); err != nil {
		t.Fatal(err)
	}
	if jobType != "extract_session" || state != "queued" {
		t.Fatalf("job=%s/%s", jobType, state)
	}
	sess, err := st.GetSession(ctx, "sess-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if sess.TranscriptOffset != int64(len(content)) {
		t.Fatalf("offset=%d, muốn %d", sess.TranscriptOffset, len(content))
	}
}
