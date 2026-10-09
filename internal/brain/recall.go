package brain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"mind-runner/internal/egress"
	"mind-runner/internal/store"
)

// Hằng pipeline recall (Task 3.3).
const (
	ftsN    = 50 // số chunk FTS lấy trước khi gom về note
	vecN    = 50 // số chunk vector lấy trước khi gom về note
	rerankM = 20 // số note tối đa đưa vào rerank

	// Ưu tiên project đang làm: cộng vào điểm rerank (0..1) / nhân điểm RRF.
	// Đủ để vượt note ngang điểm của project khác, không đủ để kéo note lạc đề lên.
	projectBoostRerank = 0.08
	projectBoostRRF    = 1.3
)

// RecallParams tham số tìm kiếm hybrid.
type RecallParams struct {
	Query    string
	SpaceIDs []int64
	Tags     []string
	Kinds    []string // rỗng = mọi kind
	Limit    int      // 0 → [recall].limit (mặc định 5)
	// MinScore: nil → [recall].min_score; 0 = tắt ngưỡng.
	MinScore *float64
	// Project: lọc cứng (chỉ khi người dùng hỏi rõ một project); "" = mọi project.
	Project string
	// Prefer: project đang làm — note cùng project được đẩy lên (không lọc).
	// "" → project của Brain (SetProject).
	Prefer string
}

// RecallHit một kết quả đã hợp nhất về note.
type RecallHit struct {
	NoteID, ChunkID     int64
	Kind, Space, Source string
	SessionID           *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Tags                []string
	Meta                store.NoteMeta // why/who/when/ref… để trích dẫn và đánh giá liên quan
	Project             string         // "" = chung
	Text                string         // text của chunk khớp tốt nhất
	Score               float64        // RRF, hoặc rerank score nếu rerank chạy
}

// RecallResult: Hits + trạng thái từng tầng — "ok:N" | "error: …" | "skipped: …".
// N là số candidate (note sau khi gom) tầng đó đóng góp vào RRF.
type RecallResult struct {
	Hits   []RecallHit
	Stages map[string]string
}

// spaceGroup: các space cùng policy — embed/vector đi một endpoint.
type spaceGroup struct {
	pol      egress.Policy
	spaceIDs []int64
}

// spaceGroups nhóm space được chọn theo policy (cloud trước, local sau — tất
// định). FTS (SQLite local) không phân biệt policy; chỉ embed/vector mới cần
// đúng endpoint từng policy.
func (b *Brain) spaceGroups(ctx context.Context, spaceIDs []int64) ([]spaceGroup, error) {
	sps, err := b.st.Spaces(ctx)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, id := range spaceIDs {
		want[id] = true
	}
	byPol := map[egress.Policy][]int64{}
	var order []egress.Policy
	for _, sp := range sps {
		if len(spaceIDs) > 0 && !want[sp.ID] {
			continue
		}
		pol := egress.Policy(b.cfg.SpacePolicy(sp.Name))
		if _, seen := byPol[pol]; !seen {
			order = append(order, pol)
		}
		byPol[pol] = append(byPol[pol], sp.ID)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return order[i] == egress.PolicyCloud && order[j] != egress.PolicyCloud
	})
	out := make([]spaceGroup, 0, len(order))
	for _, pol := range order {
		out = append(out, spaceGroup{pol: pol, spaceIDs: byPol[pol]})
	}
	return out, nil
}

// Recall pipeline hybrid: FTS top-50 + vector top-50 → gom về note (mỗi note
// giữ chunk hạng tốt nhất) → RRF → rerank top-20 → cắt Limit. Mọi suy giảm
// hiện rõ trong Stages thay vì hỏng âm thầm.
func (b *Brain) Recall(ctx context.Context, p RecallParams) (RecallResult, error) {
	ctx = egress.WithPurpose(ctx, "recall")
	limit := p.Limit
	if limit <= 0 {
		limit = 5
		if b.cfg != nil && b.cfg.Recall.Limit > 0 {
			limit = b.cfg.Recall.Limit
		}
	}
	var minScore float64
	if p.MinScore != nil {
		minScore = *p.MinScore
	} else if b.cfg != nil {
		minScore = b.cfg.Recall.MinScore
	}
	stages := map[string]string{}

	groups, err := b.spaceGroups(ctx, p.SpaceIDs)
	if err != nil {
		return RecallResult{}, err
	}
	if len(groups) == 0 {
		// Không space nào được chọn — trả rỗng. LƯU Ý: không để rơi vào quy
		// ước "spaceIDs rỗng = mọi space" của store (sai ngữ nghĩa bảo mật).
		return RecallResult{Hits: []RecallHit{}, Stages: map[string]string{
			"fts":    "skipped: no spaces",
			"vector": "skipped: no spaces",
			"rerank": "skipped: no candidates",
		}}, nil
	}
	var allIDs []int64
	for _, g := range groups {
		allIDs = append(allIDs, g.spaceIDs...)
	}

	// (1) FTS — query chỉ có ký tự đặc biệt thì bỏ qua.
	var ftsList []int64
	ftsChunk := map[int64]int64{}
	if store.SanitizeFTSQuery(p.Query) == "" {
		stages["fts"] = "skipped: empty query tokens"
	} else {
		hits, err := b.st.SearchFTS(ctx, p.Query, store.NoteFilter{
			SpaceIDs: allIDs, Tags: p.Tags, Kinds: p.Kinds, Project: p.Project}, ftsN)
		if err != nil {
			return RecallResult{}, err
		}
		for _, h := range hits {
			if _, seen := ftsChunk[h.NoteID]; !seen {
				ftsList = append(ftsList, h.NoteID)
				ftsChunk[h.NoteID] = h.ChunkID
			}
		}
		stages["fts"] = fmt.Sprintf("ok:%d", len(ftsList))
	}

	// (2) Vector: mỗi policy một lần embed query + quét với model của policy
	// đó. Lỗi một policy → stage partial/error cho đúng nhóm đó; KHÔNG có nhánh rơi sang policy khác (spec: không fallback ngầm).
	var vecList []int64
	vecChunk := map[int64]int64{}
	type vecOutcome struct {
		pol egress.Policy
		ok  bool
		n   int
		err string
	}
	var outs []vecOutcome
	for _, g := range groups {
		if b.eg == nil { // Brain dựng không egress (hook/export/test) → chỉ FTS
			break
		}
		model := b.eg.EmbedModel(g.pol)
		qv, err := b.eg.Embed(ctx, g.pol, []string{p.Query})
		if err != nil {
			// không enqueue backfill ở đây (mỗi recall sẽ đẻ job trùng không giới
			// hạn) — WriteNote + maintenance backfill đã lo.
			outs = append(outs, vecOutcome{pol: g.pol, err: shortErr(err)})
			continue
		}
		hits, err := b.VectorTop(ctx, model, store.NoteFilter{
			SpaceIDs: g.spaceIDs, Tags: p.Tags, Kinds: p.Kinds, Project: p.Project}, qv[0], vecN)
		if err != nil {
			return RecallResult{}, err
		}
		n0 := len(vecList)
		for _, h := range hits {
			if _, seen := vecChunk[h.NoteID]; !seen {
				vecList = append(vecList, h.NoteID)
				vecChunk[h.NoteID] = h.ChunkID
			}
		}
		outs = append(outs, vecOutcome{pol: g.pol, ok: true, n: len(vecList) - n0})
	}
	// Stage string: 1 policy → "ok:N"/"error: …"; nhiều policy → "ok:N" |
	// "partial: cloud=12, local: error <msg>" | "error: …".
	if b.eg == nil {
		stages["vector"] = "skipped: no egress"
	} else if len(outs) == 1 {
		if outs[0].ok {
			stages["vector"] = fmt.Sprintf("ok:%d", outs[0].n)
		} else {
			stages["vector"] = "error: " + outs[0].err
		}
	} else {
		var parts []string
		nErr, total := 0, 0
		for _, o := range outs {
			if o.ok {
				parts = append(parts, fmt.Sprintf("%s=%d", o.pol, o.n))
				total += o.n
			} else {
				parts = append(parts, fmt.Sprintf("%s: error %s", o.pol, o.err))
				nErr++
			}
		}
		switch {
		case nErr == 0:
			stages["vector"] = fmt.Sprintf("ok:%d", total)
		case nErr == len(outs):
			stages["vector"] = "error: " + strings.Join(parts, "; ")
		default:
			stages["vector"] = "partial: " + strings.Join(parts, ", ")
		}
	}

	// (3) RRF → top rerankM. Chunk đại diện mỗi note: ưu tiên chunk khớp FTS
	// (hạng từ khoá rõ nghĩa hơn), không có thì lấy chunk vector.
	fused := Fuse(ftsList, vecList)
	if len(fused) > rerankM {
		fused = fused[:rerankM]
	}
	type cand struct {
		Fused
		chunkID int64
		text    string
		score   float64
	}
	cands := make([]cand, 0, len(fused))
	ids := make([]int64, 0, len(fused))
	for _, f := range fused {
		c := cand{Fused: f, score: f.Score}
		if ch, ok := ftsChunk[f.ID]; ok {
			c.chunkID = ch
		} else {
			c.chunkID = vecChunk[f.ID]
		}
		ids = append(ids, c.chunkID)
		cands = append(cands, c)
	}
	if len(cands) > 0 {
		texts, err := b.st.ChunkTextsByIDs(ctx, ids)
		if err != nil {
			return RecallResult{}, err
		}
		for i := range cands {
			cands[i].text = texts[cands[i].chunkID]
		}
	}
	prefer := p.Prefer
	if prefer == "" {
		prefer = b.project
	}
	var sameProj map[int64]bool
	if prefer != "" && len(cands) > 0 {
		noteIDs := make([]int64, len(cands))
		for i, c := range cands {
			noteIDs[i] = c.ID
		}
		projs, err := b.st.NoteProjects(ctx, noteIDs)
		if err != nil {
			return RecallResult{}, err
		}
		sameProj = make(map[int64]bool, len(projs))
		for id, pr := range projs {
			sameProj[id] = pr == prefer
		}
	}
	// boosted: điểm dùng để XẾP — cùng project được cộng thêm, điểm báo ra
	// (và ngưỡng min_score) vẫn là điểm gốc để không méo hiệu chuẩn.
	boosted := func(id int64, score float64, reranked bool) float64 {
		if !sameProj[id] {
			return score
		}
		if reranked {
			return score + projectBoostRerank
		}
		return score * projectBoostRRF
	}

	reranked := false
	// (4) Rerank — chỉ chạy khi mọi space cùng policy (docs đi một endpoint,
	// không trộn nguồn); lỗi/model rỗng → giữ thứ tự RRF (hiện ở Stages).
	if len(cands) == 0 {
		stages["rerank"] = "skipped: no candidates"
	} else if b.eg == nil {
		stages["rerank"] = "skipped: no egress"
	} else if len(groups) > 1 {
		stages["rerank"] = "skipped: mixed policy"
	} else {
		docs := make([]string, len(cands))
		for i, c := range cands {
			docs[i] = c.text
		}
		scores, err := b.eg.Rerank(ctx, groups[0].pol, p.Query, docs)
		if err != nil {
			stages["rerank"] = "skipped: " + shortErr(err)
		} else {
			for i := range cands {
				cands[i].score = scores[i]
			}
			// Ngưỡng chỉ áp trên điểm rerank (đã hiệu chuẩn 0..1): bỏ kết quả
			// không liên quan thay vì luôn trả đủ limit → ít token nhiễu.
			kept := cands[:0]
			for _, c := range cands {
				if minScore <= 0 || c.score >= minScore {
					kept = append(kept, c)
				}
			}
			if dropped := len(cands) - len(kept); dropped > 0 {
				stages["rerank"] = fmt.Sprintf("ok:%d (bỏ %d dưới ngưỡng %.2f)", len(kept), dropped, minScore)
			} else {
				stages["rerank"] = fmt.Sprintf("ok:%d", len(kept))
			}
			cands = kept
			sort.SliceStable(cands, func(i, j int) bool {
				si, sj := boosted(cands[i].ID, cands[i].score, true), boosted(cands[j].ID, cands[j].score, true)
				if si != sj {
					return si > sj
				}
				return cands[i].chunkID < cands[j].chunkID
			})
			reranked = true
		}
	}
	if !reranked && sameProj != nil {
		sort.SliceStable(cands, func(i, j int) bool {
			return boosted(cands[i].ID, cands[i].score, false) > boosted(cands[j].ID, cands[j].score, false)
		})
	}
	if len(cands) > limit {
		cands = cands[:limit]
	}

	// (5) Dựng hits: metadata note + tên space + text chunk.
	if len(cands) == 0 {
		return RecallResult{Hits: []RecallHit{}, Stages: stages}, nil
	}
	sps, err := b.st.Spaces(ctx)
	if err != nil {
		return RecallResult{}, err
	}
	spaceName := make(map[int64]string, len(sps))
	for _, sp := range sps {
		spaceName[sp.ID] = sp.Name
	}
	hits := make([]RecallHit, 0, len(cands))
	for _, c := range cands {
		n, err := b.st.FetchNote(ctx, c.ID)
		if err != nil {
			return RecallResult{}, err
		}
		hits = append(hits, RecallHit{
			NoteID:    n.ID,
			ChunkID:   c.chunkID,
			Kind:      n.Kind,
			Space:     spaceName[n.SpaceID],
			Source:    n.Source,
			SessionID: n.SessionID,
			CreatedAt: n.CreatedAt,
			UpdatedAt: n.UpdatedAt,
			Tags:      n.Tags,
			Meta:      n.Meta,
			Project:   n.Project,
			Text:      c.text,
			Score:     c.score,
		})
	}
	return RecallResult{Hits: hits, Stages: stages}, nil
}

// shortErr gọt thông điệp lỗi cho Stages (≤120 rune).
func shortErr(err error) string {
	rs := []rune(err.Error())
	if len(rs) > 120 {
		return string(rs[:120])
	}
	return string(rs)
}
