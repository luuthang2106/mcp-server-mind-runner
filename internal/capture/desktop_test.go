package capture

import (
	"context"
	"testing"
	"time"
)

func TestResolveSessionDesktopHeuristic(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id1, err := ResolveSession(ctx, st, ClientDesktop, 1, t0)
	if err != nil || id1 == "" {
		t.Fatalf("id1=%q err=%v", id1, err)
	}

	// trong 30' → reuse + touch
	id2, err := ResolveSession(ctx, st, ClientDesktop, 1, t0.Add(29*time.Minute))
	if err != nil || id2 != id1 {
		t.Fatalf("id2=%q err=%v, muốn reuse %q", id2, err, id1)
	}
	sess, err := st.GetSession(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.LastSeenAt.Equal(t0.Add(29 * time.Minute)) {
		t.Fatalf("last_seen=%v", sess.LastSeenAt)
	}

	// quá 30' kể từ lần cuối → session mới
	id3, err := ResolveSession(ctx, st, ClientDesktop, 1, t0.Add(29*time.Minute+31*time.Minute))
	if err != nil || id3 == id1 {
		t.Fatalf("id3=%q err=%v, muốn session mới", id3, err)
	}

	// space khác → session riêng
	id4, err := ResolveSession(ctx, st, ClientDesktop, 2, t0)
	if err != nil || id4 == id1 || id4 == id3 {
		t.Fatalf("id4=%q err=%v", id4, err)
	}

	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("sessions=%d, muốn 3", n)
	}
}
