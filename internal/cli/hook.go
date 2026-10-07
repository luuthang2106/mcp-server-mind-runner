package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/capture"
	"mind-runner/internal/config"
	"mind-runner/internal/logging"
	"mind-runner/internal/project"
)

// RunHook là entry cho Claude Code hooks. Nguyên tắc: hook không bao giờ được
// làm hỏng Claude — mọi lỗi runtime ghi logs/hook.err.log và vẫn exit 0;
// lỗi cấu hình in stderr nhưng cũng exit 0 (capture là best-effort).
func RunHook(args []string, stdout, stderr io.Writer, env func(string) string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mind-runner hook session-start|prompt|stop|session-end [--snapshot] [--client NAME]")
		return 2
	}
	sub := args[0]
	switch sub {
	case "session-start", "prompt", "stop", "session-end":
	default:
		fmt.Fprintf(stderr, "hook: subcommand lạ %q (session-start|prompt|stop|session-end)\n", sub)
		return 2
	}

	opt := hookOpts{client: capture.ClientCode, env: env}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--snapshot":
			opt.snapshot = true
		case "--client":
			if i+1 < len(args) && args[i+1] != "" {
				opt.client = args[i+1]
				i++
			}
		}
	}

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "hook:", err) // không thể tiếp tục nhưng vẫn exit 0
		return 0
	}
	base := config.ExpandHome(cfg.DataDir)

	var hookErr error
	switch sub {
	case "session-start":
		hookErr = hookBriefing(cfg, stdout, "SessionStart", opt)
	case "prompt":
		hookErr = hookBriefing(cfg, stdout, "UserPromptSubmit", opt)
	case "stop":
		hookErr = hookStop(cfg, base, false, opt)
	case "session-end":
		hookErr = hookStop(cfg, base, true, opt)
	}
	if hookErr != nil {
		if lg, lerr := logging.New(base, "hook.err", "error"); lerr == nil {
			lg.Error("hook", "sub", sub, "err", hookErr.Error())
		} else {
			fmt.Fprintln(stderr, "hook:", hookErr)
		}
	}
	return 0
}

// hookOpts: cờ dòng lệnh của hook.
//
// snapshot: client (ZCode) không đưa transcript thật mà ghi một file tạm mới
// cho MỖI lần gọi hook, chỉ chứa lượt hiện tại (prompt ở UserPromptSubmit,
// câu trả lời ở Stop). Khi đó chốt nguyên file mỗi lần gọi thay vì đọc delta
// theo offset, và không lưu đường dẫn tạm vào session.
type hookOpts struct {
	snapshot bool
	client   string
	env      func(string) string
}

// projectDir: cwd của phiên; client không gửi "cwd" thì lấy biến môi trường
// thư mục dự án mà client đặt cho hook.
func (o hookOpts) projectDir(cwd string) string {
	if cwd != "" || o.env == nil {
		return cwd
	}
	for _, k := range []string{"CLAUDE_PROJECT_DIR", "ZCODE_PROJECT_DIR"} {
		if v := o.env(k); v != "" {
			return v
		}
	}
	return ""
}

// hookBriefing phục vụ SessionStart và UserPromptSubmit: ghi nhận session rồi
// chèn briefing qua additionalContext nếu space chưa được brief trong "ngày
// làm việc" hiện tại (ranh giới = [briefing].day_start_hour).
//
// UserPromptSubmit là thứ làm recap "đầu ngày" thay vì "session đầu tiên":
// session mở từ tối qua (gập máy, mở lại sáng nay) không chạy lại SessionStart,
// nhưng prompt đầu tiên của ngày mới vẫn đi qua hook này. Đường nóng (đã brief)
// chỉ tốn 1 SELECT meta, không dựng văn bản, không in gì.
func hookBriefing(cfg config.Config, stdout io.Writer, event string, opt hookOpts) error {
	var ev struct {
		SessionID      string `json:"session_id"`
		CWD            string `json:"cwd"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.NewDecoder(stdin).Decode(&ev); err != nil {
		return fmt.Errorf("đọc stdin: %w", err)
	}
	if ev.SessionID == "" {
		return errors.New("thiếu session_id")
	}

	ctx := context.Background()
	st, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	ev.CWD = opt.projectDir(ev.CWD)
	spaceID, err := st.SpaceByName(ctx, cfg.MatchSpace(ev.CWD))
	if err != nil {
		return err
	}
	var tp *string
	if ev.TranscriptPath != "" && !opt.snapshot {
		tp = &ev.TranscriptPath
	}
	now := time.Now()
	// upsert cả ở prompt: session bắt đầu trước khi cài hook vẫn được capture.
	if err := st.UpsertSessionStart(ctx, ev.SessionID, opt.client, spaceID, tp, now); err != nil {
		return err
	}
	proj := project.Of(ev.CWD)
	if err := st.SetSessionCWD(ctx, ev.SessionID, ev.CWD, proj); err != nil {
		return err
	}
	if opt.snapshot && event == "UserPromptSubmit" && ev.TranscriptPath != "" {
		if _, err := capture.Snapshot(ctx, st, ev.SessionID, ev.TranscriptPath, cfg.Retention.RawDays, now); err != nil {
			return err
		}
	}

	b := brain.New(st, nil, &cfg)
	b.SetBriefingBudget(cfg.Briefing.TokenBudget)
	b.SetProject(proj)
	if done, err := b.BriefedToday(ctx, spaceID, now); err != nil || done {
		return err
	}
	br, err := b.Briefing(ctx, brain.BriefingParams{SpaceID: spaceID, Now: now})
	if err != nil {
		return err
	}
	if !br.Delivered || br.Text == "" {
		return nil
	}
	text := br.Text
	if event == "UserPromptSubmit" {
		text = "[mind-runner] Recap đầu ngày (" + b.BriefingDay(now) + ") — dùng làm ngữ cảnh, " +
			"không cần nhắc lại nếu không liên quan tới câu hỏi:\n\n" + text
	}
	line, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     event,
			"additionalContext": text,
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(line))
	return nil
}

func hookStop(cfg config.Config, base string, sessionEnd bool, opt hookOpts) error {
	var ev struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.NewDecoder(stdin).Decode(&ev); err != nil {
		return fmt.Errorf("đọc stdin: %w", err)
	}
	if ev.SessionID == "" {
		return errors.New("thiếu session_id")
	}

	st, err := openDB(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	sess, err := st.GetSession(ctx, ev.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // session-start chưa chạy → không có gì để chốt
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if opt.snapshot {
		if ev.TranscriptPath != "" {
			if _, err := capture.Snapshot(ctx, st, sess.ID, ev.TranscriptPath, cfg.Retention.RawDays, now); err != nil {
				return err
			}
		}
	} else {
		if ev.TranscriptPath != "" {
			sess.TranscriptPath = &ev.TranscriptPath
		}
		if _, err := capture.Stop(ctx, st, sess, cfg.Retention.RawDays, filepath.Join(base, "spool"), now); err != nil {
			return err
		}
	}
	if sessionEnd {
		// phiên đã đóng: không chờ hết thời gian gộp
		if err := st.ExpediteExtract(ctx, sess.ID, now); err != nil {
			return err
		}
		if _, err := st.Enqueue(ctx, "summarize_session", map[string]string{"session_id": sess.ID}, now); err != nil {
			return err
		}
	}
	return nil
}
