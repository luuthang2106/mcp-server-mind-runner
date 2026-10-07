package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/execx"
	"mind-runner/internal/media"
)

// reorderIngestArgs đưa cờ về trước positional để flag.Parse nhận cả dạng
// usage quảng cáo `ingest <path> [--space <name>] [--kind <kind>]` (flag dừng
// phân tích ở arg không phải cờ đầu tiên). Biết trước cờ nào nhận giá trị rời.
func reorderIngestArgs(args []string) []string {
	valueFlags := map[string]bool{"--space": true, "--kind": true, "-space": true, "-kind": true}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

// RunIngest: mind-runner ingest <path> [--space <name>] [--kind <kind>].
// CLI không tạo session (chỉ Desktop event mới mở/touch session).
func RunIngest(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	space := fs.String("space", "", "space chứa note (mặc định theo config)")
	kind := fs.String("kind", "", "note|fact|preference|decision|task_hint (mặc định note)")
	if err := fs.Parse(reorderIngestArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: mind-runner ingest <path> [--space <name>] [--kind <kind>]")
		return 2
	}
	path := fs.Arg(0)

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "ingest:", err)
		return 1
	}
	base := config.ExpandHome(cfg.DataDir)
	st, err := openDB(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "ingest:", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	spaceName := *space
	if spaceName == "" {
		spaceName = cfg.Spaces.Default
	}
	spaceID, err := st.SpaceIDOrList(ctx, spaceName)
	if err != nil {
		fmt.Fprintln(stderr, "ingest:", err)
		return 1
	}

	eg := egress.New(cfg, nil)
	attachUsageLog(eg, base)
	defer eg.FlushUsage()
	if media.IsMedia(path) {
		md := media.New(st, brain.New(st, eg, &cfg), eg, &cfg, execx.OS{}, base)
		res, err := md.Ingest(ctx, path, spaceID, "cli")
		if err != nil {
			fmt.Fprintln(stderr, "ingest:", err)
			return 1
		}
		if res.Created {
			fmt.Fprintf(stdout, "media %d queued\n", res.MediaID)
		} else {
			fmt.Fprintf(stdout, "media %d %s (đã có)\n", res.MediaID, res.Status)
		}
		return 0
	}

	b := brain.New(st, eg, &cfg)
	res, err := b.IngestText(ctx, brain.IngestTextParams{Path: path, SpaceID: spaceID, Kind: *kind})
	if err != nil {
		fmt.Fprintln(stderr, "ingest:", err)
		return 1
	}
	if res.Fresh {
		fmt.Fprintf(stdout, "note_id %d\n", res.NoteID)
	} else {
		fmt.Fprintf(stdout, "note_id %d (đã có)\n", res.NoteID)
	}
	return 0
}
