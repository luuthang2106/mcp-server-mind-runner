// Package execx chạy lệnh ngoài (launchctl, claude, sips, avconvert, afconvert…)
// qua một interface duy nhất để test bằng fake.
package execx

import (
	"context"
	"os/exec"
	"time"
)

// Runner chạy một lệnh và trả stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OS là impl thật.
type OS struct{}

func (OS) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// ctx hết hạn → kill; pipe con cháu giữ stdout thì chờ tối đa 5s rồi bỏ.
	cmd.WaitDelay = 5 * time.Second
	return cmd.Output()
}
