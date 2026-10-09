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
const Instructions = `mind-runner is the user's long-term memory. Nothing is automatic: you decide when to call these tools, without being asked.
1. briefing: call when a chat opens with a greeting, start of day or "what's pending", or on a recap request (force=true); not before a concrete task. Use what matters; delivered=false → don't retry.
2. Recall BEFORE answering when the answer may depend on the user's past: "last time", "what did we decide/why", their docs/meetings/recordings, their projects/people/terms you don't know. When unsure, recall. Use 2-5 specific keywords; narrow with kinds (["decision"], ["document"]); cite source/when/ref. 0 hits → retry once with synonyms or the other language (Vietnamese/English); still empty → say it isn't saved, never guess. Not for general knowledge or code in the repo.
3. Save the moment it happens, without asking. Bar: would a fresh session a month from now need this? remember kind=decision (+why) · fact (as_of if it can change) · preference · procedure (repeatable steps) · note (event); concrete open loop → task_add; progress → task_update. After finishing a piece of work, check once for decisions, completions, follow-ups. Skip small talk, in-session details, anything in the repo, secret values (save where the credential lives instead).
4. Write one self-contained sentence in the user's language; fill only what the user stated, never invent why/who/when/due. Then one line: "saved: decision #123".
5. Changed decision/fact → recall it, then remember with supersedes=<id> (the old note is hidden). forget/ingest only on request; confirm before forget.
6. Message prefixes: "mind:" remember · "mind recall:" recall · "mind todo:" task_add · "mind fix:" correct (task_update, or supersedes) · "mind forget:" forget. "mind update" = update the mind-runner software, not memory: call no memory tools; give the running version and the update steps from INSTALL.md.
Project labels are automatic; pass project only for a different project.`

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
