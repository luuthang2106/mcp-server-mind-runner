// Package server dựng MCP server (stdio) của mind-runner: các tool bộ nhớ
// (remember, …). Handler trả lỗi thường → SDK đóng gói thành tool error
// (IsError=true), giao thức vẫn OK.
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mind-runner/internal/brain"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
	"mind-runner/internal/version"
)

// Instructions gửi cho client lúc initialize (MCP "instructions"). Client hỗ
// trợ (Claude Code, Claude Desktop, nhiều agent khác) tự đưa vào system
// prompt — người dùng KHÔNG cần dán custom instructions thủ công.
const Instructions = `mind-runner is the user's long-term memory (second brain): decisions, facts, preferences, tasks, past sessions, and documents/recordings/images they ingested. Use it proactively — the user should not have to ask or use keywords.

Start: call briefing once per conversation. delivered=false means today's recap was already given (possibly by a hook) — do not retry.

Capture as it happens (do not wait for the end of the conversation, do not ask permission for routine saves):
- A choice was made → remember kind=decision, with why (and alternatives) if stated. If it replaces an earlier decision, recall it and pass supersedes=<note_id>.
- A stable truth about their projects, people, setup → kind=fact (as_of if it can change, ref for the source).
- How they like things done → kind=preference (scope if stated).
- A notable event or meeting outcome → kind=note (who/when if stated).
- A concrete open loop (they will do / someone owes them) → task_add with due (YYYY-MM-DD), owner/waiting_on, why, next_step when stated. Progress on a known task → task_update.
Write text as one self-contained sentence in the user's language. Fill only what the user said — never invent why/who/when/due. Skip small talk, transient details, things already in the repo, and secrets.

Recall before answering when the answer may depend on what they saved or did before: past references ("last time", "what did we decide", "why did we pick X"), their documents/meetings/recordings, or their own projects, people and terms you are unsure of. When unsure, recall — it is cheaper than a wrong answer.
Use specific keywords and narrow with kinds (["document"] for files, ["decision"] for rationale). Cite source/when/ref. Empty result = not saved: say so, never fabricate. Not for general knowledge or code open in the repo.

Ingest files only when the user asks (recordings with other people need their consent). Forget only on request, after confirming what will be removed.
Omit space unless the user names one — the server picks the default.`

// New tạo MCP server với tên/phiên bản lấy từ version.String().
func New(b *brain.Brain, st *store.Store, md *media.Media) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mind-runner", Version: version.String()},
		&mcp.ServerOptions{Instructions: Instructions})
	def := b.DefaultSpace()
	registerRemember(srv, b, st, def)
	registerRecall(srv, b, st, def)
	registerBriefing(srv, b, st, def)
	registerTaskAdd(srv, st, def)
	registerTaskUpdate(srv, st)
	registerTaskList(srv, st, def)
	registerIngest(srv, b, st, md, def)
	registerExport(srv, b)
	registerForget(srv, st)
	registerPrompts(srv)
	return srv
}
