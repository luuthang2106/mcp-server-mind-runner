package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts đăng ký 3 prompt mẫu (7.4). Claude Code dùng được như slash
// command; Claude Desktop không hiện MCP prompts — coi là bonus (spec §9).
func registerPrompts(srv *mcp.Server) {
	spaceArg := &mcp.PromptArgument{
		Name:        "space",
		Description: "Space cần dùng (bỏ trống = mặc định)",
	}
	add := func(name, desc string, text func(space string) string) {
		srv.AddPrompt(&mcp.Prompt{
			Name:        name,
			Description: desc,
			Arguments:   []*mcp.PromptArgument{spaceArg},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: desc,
				Messages: []*mcp.PromptMessage{
					{Role: "user", Content: &mcp.TextContent{Text: text(req.Params.Arguments["space"])}},
				},
			}, nil
		})
	}

	add("weekly-review", "Tóm tắt tuần từ task_list + recall",
		func(space string) string {
			return "Gọi tool `task_list` lấy việc đang mở và việc đã xong 7 ngày qua, " +
				"gọi `recall` cho các chủ đề, quyết định, sự kiện trong tuần" + spaceNote(space) +
				". Sau đó tóm tắt: (1) việc đã hoàn thành, (2) quyết định quan trọng, " +
				"(3) việc còn lại và gợi ý ưu tiên tuần tới."
		})
	add("meeting-notes", "Ghi biên bản họp: quyết định → remember kind decision, việc → task_add",
		func(space string) string {
			return "Giúp tôi ghi biên bản cuộc họp này" + spaceNote(space) + ": mỗi quyết định chốt lại " +
				"gọi `remember` với kind `decision`; mỗi việc còn lại gọi `task_add`; " +
				"cuối cùng gọi `remember` kind `note` để lưu tóm tắt cuộc họp."
		})
	add("study-session", "Học chủ đề: recall trước, điểm chính → remember kind fact",
		func(space string) string {
			return "Giúp tôi học chủ đề này" + spaceNote(space) + ": trước tiên gọi `recall` để nạp " +
				"những gì tôi đã biết liên quan; các điểm chính rút ra trong buổi học gọi `remember` " +
				"với kind `fact`."
		})
}

// spaceNote chèn định danh space vào text prompt; rỗng → không thêm gì.
func spaceNote(space string) string {
	if space == "" {
		return ""
	}
	return fmt.Sprintf(" (space: %s)", space)
}
