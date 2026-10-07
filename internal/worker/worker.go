// Package worker: vòng lặp chạy job từ bảng jobs. Claim → handle →
// Complete/Fail; không có handler → fail với lý do rõ (attempts vẫn tăng →
// dead sau MaxAttempts, hiện ở status — không im lặng). Lỗi bọc
// media.ErrPermanent (retry vô nghĩa, vd audio vượt trần) → MarkDead ngay.
// Lỗi egress.ErrNoAPIKey (process không có key — vd launchd maintenance; key
// chỉ nằm trong cấu hình MCP của client) → hoãn job, không tính lượt thử, để
// process MCP có key chạy sau.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mind-runner/internal/egress"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
)

type Handler func(ctx context.Context, payload json.RawMessage) error

type Registry struct {
	m map[string]Handler
}

func NewRegistry() *Registry { return &Registry{m: map[string]Handler{}} }

func (r *Registry) Register(jobType string, h Handler) { r.m[jobType] = h }

// Run: claim → handle → Complete/Fail; dừng khi hết job, đủ max (max <= 0 →
// không giới hạn) hoặc ctx hủy. Trả số job đã xử lý.
func Run(ctx context.Context, st *store.Store, reg *Registry, max int, now func() time.Time) (int, error) {
	n, deferred := 0, 0
	for max <= 0 || n < max {
		if ctx.Err() != nil {
			break
		}
		job, err := st.ClaimJob(ctx, now())
		if err != nil {
			return n, err
		}
		if job == nil {
			break
		}
		h, ok := reg.m[job.Type]
		if !ok {
			err = fmt.Errorf("no handler for job type %s", job.Type)
		} else {
			err = h(egress.WithPurpose(ctx, job.Type), job.Payload)
		}
		// Ghi kết quả bằng ctx không bị hủy: ctx hủy giữa chừng (client đóng
		// stdio, SIGTERM) vẫn phải chốt state, nếu không job kẹt 'running'.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		werr := finish(wctx, st, job, err, ctx.Err() != nil, now())
		cancel()
		if werr != nil {
			return n, werr
		}
		if errors.Is(err, egress.ErrNoAPIKey) {
			deferred++
			if deferred >= maxDeferredPerRun {
				break // mọi job LLM đều sẽ hoãn — đừng quét cả hàng đợi vô ích
			}
			continue
		}
		n++
	}
	return n, nil
}

// maxDeferredPerRun: số job hoãn tối đa mỗi lượt Run trước khi dừng sớm.
const maxDeferredPerRun = 20

// deferNoKey: job thiếu key được lùi lại chừng này (sweep MCP 5 phút/lần).
const deferNoKey = 10 * time.Minute

func finish(ctx context.Context, st *store.Store, job *store.Job, err error, canceled bool, now time.Time) error {
	switch {
	case err == nil:
		return st.CompleteJob(ctx, job.ID)
	case errors.Is(err, egress.ErrNoAPIKey):
		return st.DeferJob(ctx, now, job.ID, now.Add(deferNoKey), err.Error())
	case errors.Is(err, media.ErrPermanent):
		return st.MarkDead(ctx, job.ID, err.Error(), now)
	case canceled:
		// process bị hủy giữa chừng (không phải timeout của chính job) —
		// không phải lỗi của job, trả lại hàng đợi ngay
		return st.DeferJob(ctx, now, job.ID, now, err.Error())
	default:
		return st.FailJob(ctx, now, job.ID, err.Error())
	}
}
