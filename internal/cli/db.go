package cli

import (
	"context"
	"path/filepath"

	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/logging"
	"mind-runner/internal/store"
)

// openDB mở DB trong data dir và áp migration còn thiếu. Mọi entrypoint ghi
// (mcp, hook, maintenance, ingest…) đi qua đây: nâng cấp binary không cần
// chạy lại setup. Migrate khi không có gì pending chỉ tốn 2 câu SQL nhỏ.
func openDB(ctx context.Context, cfg config.Config) (*store.Store, error) {
	base := config.ExpandHome(cfg.DataDir)
	st, err := store.Open(filepath.Join(base, "mind-runner.db"))
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx, filepath.Join(base, "backups")); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// attachUsageLog bật log usage gateway vào logs/egress.log (chỉ số đo, không
// nội dung). Lỗi mở file → bỏ qua: log không được chặn chức năng chính.
func attachUsageLog(eg *egress.Egress, dataDir string) {
	if lg, err := logging.New(dataDir, "egress", "info"); err == nil {
		eg.SetUsageLog(lg)
	}
}
