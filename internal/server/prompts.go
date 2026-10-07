package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts đăng ký 3 prompt mẫu (7.4). Claude Code dùng được như slash
// command; Claude Desktop không hiện MCP prompts — coi là bonus (spec §9).
func registerPrompts(srv *mcp.Server) {
	projArg := &mcp.PromptArgument{
		Name:        "project",
		Description: "Chỉ một project (tên repo git); bỏ trống = mọi thứ, project hiện tại ưu tiên",
	}
	add := func(name, desc string, text func(project string) string) {
		srv.AddPrompt(&mcp.Prompt{
			Name:        name,
			Description: desc,
			Arguments:   []*mcp.PromptArgument{projArg},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: desc,
				Messages: []*mcp.PromptMessage{
					{Role: "user", Content: &mcp.TextContent{Text: text(req.Params.Arguments["project"])}},
				},
			}, nil
		})
	}

	add("weekly-review", "Tóm tắt tuần từ task_list + recall",
		func(project string) string {
			return "Gọi tool `task_list` lấy việc đang mở và việc đã xong 7 ngày qua, " +
				"gọi `recall` cho các chủ đề, quyết định, sự kiện trong tuần" + projectNote(project) +
				". Sau đó tóm tắt: (1) việc đã hoàn thành, (2) quyết định quan trọng, " +
				"(3) việc còn lại và gợi ý ưu tiên tuần tới."
		})
	add("meeting-notes", "Ghi biên bản họp: quyết định → remember kind decision, việc → task_add",
		func(project string) string {
			return "Giúp tôi ghi biên bản cuộc họp này" + projectNote(project) + ": mỗi quyết định chốt lại " +
				"gọi `remember` với kind `decision`; mỗi việc còn lại gọi `task_add`; " +
				"cuối cùng gọi `remember` kind `note` để lưu tóm tắt cuộc họp."
		})
	add("study-session", "Học chủ đề: recall trước, điểm chính → remember kind fact",
		func(project string) string {
			return "Giúp tôi học chủ đề này" + projectNote(project) + ": trước tiên gọi `recall` để nạp " +
				"những gì tôi đã biết liên quan; các điểm chính rút ra trong buổi học gọi `remember` " +
				"với kind `fact`."
		})
}

// projectNote chèn tên project vào text prompt; rỗng → không thêm gì.
func projectNote(project string) string {
	if project == "" {
		return ""
	}
	return fmt.Sprintf(" (project: %s — truyền project=%q cho recall/task_list)", project, project)
}
