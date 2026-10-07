package brain

import (
	"context"
	"errors"
	"time"

	"mind-runner/internal/egress"
)

// EmbedNote bù embedding cho mọi chunk thiếu theo model CỦA POLICY space chứa
// note; đã đủ → no-op (job chạy lặp vô hại).
func (b *Brain) EmbedNote(ctx context.Context, noteID int64) error {
	// Policy resolve qua config (nguồn chân lý) — cột spaces.policy chỉ là seed.
	n, err := b.st.FetchNote(ctx, noteID)
	if err != nil {
		return err
	}
	sp, err := b.st.SpaceByID(ctx, n.SpaceID)
	if err != nil {
		return err
	}
	pol := egress.Policy(b.cfg.SpacePolicy(sp.Name))

	if b.eg == nil {
		return errors.New("embed: brain không có egress")
	}
	model := b.eg.EmbedModel(pol)
	refs, err := b.st.ChunksMissingEmbeddings(ctx, noteID, model)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	texts := make([]string, len(refs))
	for i, r := range refs {
		texts[i] = r.Text
	}
	vecs, err := b.eg.Embed(ctx, pol, texts)
	if err != nil {
		return err
	}
	now := time.Now()
	for i, r := range refs {
		if err := b.st.UpsertEmbedding(ctx, r.ID, model, vecs[i], now); err != nil {
			return err
		}
	}
	return nil
}
