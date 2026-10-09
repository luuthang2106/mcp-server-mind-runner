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
const Instructions = `mind-runner is the user's long-term memory (second brain): decisions, facts, preferences, tasks, past sessions, and documents/recordings/images they ingested. Nothing is saved or loaded automatically — you decide when to call these tools. Use them proactively; the user should not have to ask.

Start: in a new conversation, call briefing before answering the first message, and weave what matters into your reply (do not dump it). delivered=false means today's recap was already shown in another conversation — do not retry; use task_list or recall if you need context.

Capture as it happens (do not wait for the end, do not ask permission for routine saves):
- A choice was made → remember kind=decision, with why (and alternatives) if stated. If it replaces an earlier decision, recall it and pass supersedes=<note_id> — the old note is permanently deleted.
- A stable truth about their projects, people, setup → kind=fact (as_of if it can change, ref for the source).
- How they like things done → kind=preference (scope if stated).
- A repeatable way of doing something ("how we do X", steps they follow) → kind=procedure.
- A notable event or meeting outcome → kind=note (who/when if stated).
- A concrete open loop (they will do / someone owes them) → task_add with due (YYYY-MM-DD), owner/waiting_on, why, next_step when stated. Progress on a known task → task_update (done when finished).
When you finish a piece of work with the user, check once: was something decided, completed or left to follow up? Save it then.
Write text as one self-contained sentence in the user's language. Fill only what the user said or confirmed — never invent why/who/when/due. Skip small talk, transient details, things already in the repo/code. Accounts are worth remembering (kind=fact): which account/username for which service or project, where its credential lives (macOS Keychain item, 1Password, env var), expiry — never the secret value itself (token, password, key). When they want a secret kept, offer Keychain (security add-generic-password -s <service> -a <account> -w — the value is typed at its prompt, never pasted into chat); when a task needs one, read it from where it lives and never echo the value. After saving, mention it in one short line (e.g. "saved: decision #123") so the user can correct it.

Recall before answering when the answer may depend on what they saved or did before: past references ("last time", "what did we decide", "why did we pick X"), their documents/meetings/recordings, or their own projects, people and terms you are unsure of. When unsure, recall — it is cheaper than a wrong answer.
Use specific keywords and narrow with kinds (["document"] for files, ["decision"] for rationale). Cite source/when/ref. Empty result = not saved: say so, never fabricate. Not for general knowledge or code open in the repo.

Shortcuts — if a message starts with one, do exactly that: "mind:" → remember the rest; "mind recall:" → recall; "mind todo:" → task_add; "mind fix:" → correct memory (task_update for a task #id, or remember with supersedes for a note — the old note is permanently deleted); "mind forget:" → forget (confirm what will be removed first). "mind update" (or "update mind-runner") means updating the mind-runner software, never memory content: do not call memory tools; tell them the running version (server info) and the steps — download the latest mind-runner-<version>.mcpb from the GitHub releases, drag it into Claude Desktop → Settings → Extensions (it also updates the binary used by other apps), then quit and reopen every app (without Claude Desktop: follow the update section of INSTALL.md).

Ingest files only when the user asks (recordings with other people need their consent). Forget only on request, after confirming what will be removed.
Everything is one memory: the server labels what you save with the current project (git repo) automatically and ranks that project first in recall/task_list. Pass project only when the user asks about a specific other project.`

// New tạo MCP server với tên/phiên bản lấy từ version.String().
func New(b *brain.Brain, st *store.Store, md *media.Media) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mind-runner", Version: version.String()},
		&mcp.ServerOptions{Instructions: Instructions})
	def := b.DefaultSpace()
	registerRemember(srv, b, st, def)
	registerRecall(srv, b, st, def)
	registerBriefing(srv, b, st, def)
	registerTaskAdd(srv, b, st, def)
	registerTaskUpdate(srv, st)
	registerTaskList(srv, b, st, def)
	registerIngest(srv, b, st, md, def)
	registerExport(srv, b)
	registerForget(srv, st)
	registerPrompts(srv)
	return srv
}
