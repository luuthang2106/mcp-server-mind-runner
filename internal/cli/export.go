package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
)

// RunExport: mind-runner export [--out <dir>] — out rỗng dùng
// data_dir/exports/<YYYYMMDD-HHMMSS>/.
func RunExport(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "thư mục đích (mặc định data_dir/exports/<thời gian>)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: mind-runner export [--out <dir>]")
		return 2
	}

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	st, err := openDB(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	defer st.Close()

	rep, err := brain.New(st, nil, &cfg).Export(context.Background(), *out)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	fmt.Fprintf(stdout, "export: %s (notes=%d tasks=%d relations=%d)\n", rep.Dir, rep.Notes, rep.Tasks, rep.Relations)
	return 0
}
