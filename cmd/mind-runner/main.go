package main

import (
	"fmt"
	"io"
	"os"

	"mind-runner/internal/cli"
	"mind-runner/internal/version"
)

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage())
		return 2
	}
	switch args[0] {
	case "setup":
		return cli.RunSetup(args[1:], stdout, stderr, os.Getenv)
	case "mcp":
		return cli.RunMCP(args[1:], stdout, stderr, os.Getenv)
	case "hook":
		return cli.RunHook(args[1:], stdout, stderr, os.Getenv)
	case "doctor":
		return cli.RunDoctor(args[1:], stdout, stderr, os.Getenv)
	case "status":
		return cli.RunStatus(args[1:], stdout, stderr, os.Getenv)
	case "maintenance":
		return cli.RunMaintenance(args[1:], stdout, stderr, os.Getenv)
	case "ingest":
		return cli.RunIngest(args[1:], stdout, stderr, os.Getenv)
	case "rules":
		return cli.RunRules(args[1:], stdout, stderr, os.Getenv)
	case "export":
		return cli.RunExport(args[1:], stdout, stderr, os.Getenv)
	case "version":
		fmt.Fprintln(stdout, version.String())
		return 0
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n\n%s", args[0], usage())
		return 2
	}
}

func usage() string {
	return `usage: mind-runner <command> [flags]

commands:
  setup        wizard cài đặt (config, dirs, DB, launchd)
  mcp          chạy MCP server (stdio)
  hook         hook Claude Code: session-start|stop|session-end
  maintenance  xử lý queue, watch dirs, purge, backup
  ingest       nạp file (text/markdown/audio/ảnh/video)
  doctor       kiểm tra sức khoẻ
  status       trạng thái + dung lượng
  rules        ghi khối quy tắc vào AGENTS.md/GEMINI.md của agent (--print, --uninstall)
  export       xuất Markdown
  version      in phiên bản
`
}

func main() { os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr)) }
