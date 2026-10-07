package capture

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"mind-runner/internal/store"
)

// Client cố định cho từng luồng.
const (
	ClientCode    = "claude-code"
	ClientDesktop = "claude-desktop"
)

// sessionReuse: Desktop không đầu phiên rõ ràng — heuristic nối tiếp khi
// lần cuối gặp ≤ 30 phút (spec §5).
const sessionReuse = 30 * time.Minute

// ResolveSession trả session cho luồng Desktop: session gần nhất cùng
// client+space trong vòng 30' → reuse (Touch); ngược lại tạo uuid mới.
func ResolveSession(ctx context.Context, st *store.Store, client string, spaceID int64, now time.Time) (string, error) {
	sess, err := st.LatestSession(ctx, client, spaceID)
	if err != nil {
		return "", err
	}
	if sess != nil && now.Sub(sess.LastSeenAt) <= sessionReuse {
		if err := st.TouchSession(ctx, sess.ID, now); err != nil {
			return "", err
		}
		return sess.ID, nil
	}
	id := newUUID()
	if err := st.UpsertSessionStart(ctx, id, client, spaceID, nil, now); err != nil {
		return "", err
	}
	return id, nil
}

// newUUID tạo UUIDv4 không cần dependency.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("desktop-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
