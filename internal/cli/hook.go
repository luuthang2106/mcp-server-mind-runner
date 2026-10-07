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
)

// RunHook là entry cho Claude Code hooks. Nguyên tắc: hook không bao giờ được
// làm hỏng Claude — mọi lỗi runtime ghi logs/hook.err.log và vẫn exit 0;
// lỗi cấu hình in stderr nhưng cũng exit 0 (capture là best-effort).
func RunHook(args []string, stdout, stderr io.Writer, env func(string) string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mind-runner hook session-start|prompt|stop|session-end")
		return 2
	}
	sub := args[0]
	switch sub {
	case "session-start", "prompt", "stop", "session-end":
	default:
		fmt.Fprintf(stderr, "hook: subcommand lạ %q (session-start|prompt|stop|session-end)\n", sub)
		return 2
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
		hookErr = hookBriefing(cfg, stdout, "SessionStart")
	case "prompt":
		hookErr = hookBriefing(cfg, stdout, "UserPromptSubmit")
	case "stop":
		hookErr = hookStop(cfg, base, false)
	case "session-end":
		hookErr = hookStop(cfg, base, true)
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

// hookBriefing phục vụ SessionStart và UserPromptSubmit: ghi nhận session rồi
// chèn briefing qua additionalContext nếu space chưa được brief trong "ngày
// làm việc" hiện tại (ranh giới = [briefing].day_start_hour).
//
// UserPromptSubmit là thứ làm recap "đầu ngày" thay vì "session đầu tiên":
// session mở từ tối qua (gập máy, mở lại sáng nay) không chạy lại SessionStart,
// nhưng prompt đầu tiên của ngày mới vẫn đi qua hook này. Đường nóng (đã brief)
// chỉ tốn 1 SELECT meta, không dựng văn bản, không in gì.
func hookBriefing(cfg config.Config, stdout io.Writer, event string) error {
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

	spaceID, err := st.SpaceByName(ctx, cfg.MatchSpace(ev.CWD))
	if err != nil {
		return err
	}
	var tp *string
	if ev.TranscriptPath != "" {
		tp = &ev.TranscriptPath
	}
	now := time.Now()
	// upsert cả ở prompt: session bắt đầu trước khi cài hook vẫn được capture.
	if err := st.UpsertSessionStart(ctx, ev.SessionID, capture.ClientCode, spaceID, tp, now); err != nil {
		return err
	}

	b := brain.New(st, nil, &cfg)
	b.SetBriefingBudget(cfg.Briefing.TokenBudget)
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

func hookStop(cfg config.Config, base string, sessionEnd bool) error {
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
	if ev.TranscriptPath != "" {
		sess.TranscriptPath = &ev.TranscriptPath
	}

	now := time.Now()
	if _, err := capture.Stop(ctx, st, sess, cfg.Retention.RawDays, filepath.Join(base, "spool"), now); err != nil {
		return err
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
