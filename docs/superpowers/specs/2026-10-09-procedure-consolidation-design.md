# Design: kind `procedure` + job `consolidate` + bỏ hẳn supersede (xóa cứng)

Ngày: 2026-10-09 · Trạng thái: chờ duyệt · Nguồn ý tưởng: odysseus (chỉ lấy ý tưởng, không dùng code — AGPL-3)

## 0. Chốt với người dùng

- **Procedure**: chỉ text 1 câu, không field steps, không UI riêng — extractor nhận diện "cách làm" rồi lưu.
- **Consolidation**: job định kỳ tuần một lần, mặc định BẬT; gộp nhóm trùng; không CLI thủ công.
- **Xóa cứng, không xóa mềm — bỏ hẳn supersede**: dedupe lúc extract, consolidation và `remember supersedes` (mind fix) đều xóa cứng note cũ; purge quét sạch row superseded cũ; bỏ `include_superseded` khỏi recall. Phục hồi chỉ từ backup ngày (giữ 7 bản).
- `forget` giữ nguyên (xóa mềm + ân hạn `jobs_days`; purge đã xóa thật sau đó — có giới hạn, không phình).

## A. Kind `procedure` (tầng tri thức quy trình)

Một "cách làm" người dùng mô tả — quy trình, lệnh, mẹo lặp lại được — lưu **1 câu tự chứa** bằng tiếng người dùng; vẫn nhận why/when/ref/scope qua meta như các kind khác. Là **tri thức**: không bao giờ purge theo tuổi (purge chỉ đụng events kind); tham gia dedupe lúc extract và consolidation; "thay thế nhau" = ghi mới + xóa cứng cũ như decision/fact/preference.

Không đổi schema — kind là text tự do, `LatestSchema` giữ 8.

| File | Thay đổi |
|---|---|
| `internal/brain/write.go` (~38) | `validKinds` + `"procedure"` |
| `internal/extract/parse.go` (~65) | `extractKinds` + `"procedure"` |
| `internal/extract/dedupe.go` (~41) | `knowledgeKinds` + `"procedure"` |
| `internal/extract/prompts/extract.md` | mô tả kind: chỉ khi người dùng thực sự mô tả cách làm; 1 câu, giữ tên lệnh/đường dẫn/số liệu |
| `internal/server/tools.go` | remember: switch kind + schema desc thêm procedure; recall: error string danh sách kind |
| `internal/server/server.go` (Instructions) | thêm bullet capture kind=procedure; ví dụ recall "how did we do X" |
| `README.md` | bảng kind + đoạn recall |

Không đổi: `ingestKinds` (file ingest vẫn document/…; procedure đi qua extractor + remember), briefing (`capturedLines` đã in `[kind]` generic), export.

## B. Job `consolidate` (LLM audit toàn kho định kỳ)

Gộp các note tri thức trùng/lặp về cùng một ý thành 1 note sạch; tuần 1 lần; không cần người dùng thao tác.

### B1. Config

```toml
[consolidate]
every_days = 7   # 0 hoặc âm = tắt
```

`config.Config` + struct `Consolidate`; `Default()` = 7 (file config cũ thiếu mục → mặc định BẬT). README config reference cập nhật.

### B2. Enqueue (maintenance)

Bước mới **(3b)** trong `RunMaintenance`, SAU queue (3), trước watch dirs (đổi thành 3c). Lý do đặt sau queue: tiến trình launchd chạy không key — enqueue trước queue chỉ tổ bị claim rồi hoãn 10' vô ích và làm lệch đếm `queue: processed` của test hiện có; đặt sau queue thì job nằm chờ sweep MCP (có key) hoặc maintenance lần sau.

- Đọc meta `last_consolidate_enqueue` (RFC3339). Chưa có → tới hạn ngay (maintenance đầu tiên sau nâng cấp enqueue luôn).
- Tới hạn → `Enqueue(ctx, "consolidate", nil, now)`; **idempotent theo (type,payload) sẵn có chính là pending-check** — job đang queued/failed/running thì trả id cũ, không cần query riêng. Sau đó `SetMeta(last_consolidate_enqueue, now)`.
- In: `consolidate: enqueued` hoặc `consolidate: chưa tới hạn (còn N ngày)`.
- Process keyless (launchd) vẫn enqueue được — job chỉ là row; MCP sweep có key (hoặc maintenance có key) claim sau. `ErrNoAPIKey` → DeferJob(+10 phút) như cơ chế sẵn có.

### B3. Handler

- Package mới `internal/consolidate` (mirror `extract`): `New(st, b, eg, cfg)` + `Run(ctx) error`, embed `prompts/consolidate.md`. Đăng ký trong `worker/register.go`: `reg.Register("consolidate", …)`.
- Phạm vi: mọi space, policy theo space (Chat đúng endpoint cloud/local như extract); note **active** (`deleted_at IS NULL`, `status='active'`), kind ∈ `knowledgeKinds` (decision/fact/preference/procedure).
- Batch theo **(space, project, kind)**: gộp chỉ có nghĩa trong cùng kind (validate bắt buộc), tách kind để batch đúng phạm vi và model không phải lọc nhiễu. Trong mỗi partition load theo id, đóng gói lô ≤ ~20k rune theo biên dòng (kiểu `windows()` của extract). Partition nhỏ = 1 lô.
- Mỗi lô: `Chat(pol, system=consolidatePrompt, user=notesBlock)` → JSON thuần `{"merges":[{"kind":"…","text":"…","notes":[1,2]}]}`.
- Input mỗi dòng: `#id [kind] (YYYY-MM-DD) tags text`.

### B4. Prompt (`prompts/consolidate.md`)

- Tìm nhóm note **diễn đạt lại cùng một việc** (khác chữ, cùng ý) hoặc note cũ đã bị note mới hơn thay thế (thông tin mới nhất thắng).
- Chỉ trả nhóm thật sự nên gộp; không bịa; không gộp chủ đề khác nhau dù gần chữ; giữ nguyên ngôn ngữ người dùng; text gộp = 1 câu tự chứa, giữ tên/số/ngày/đường dẫn quan trọng; nhồi ngữ cảnh cần thiết (vd why của decision) vào text vì meta không được copy.
- `"merges":[]` khi không có gì để gộp. JSON thuần đúng schema.

### B5. Validation + guard

- Mỗi merge: kind ∈ knowledgeKinds và **các note cùng kind với merge**; `notes` ≥ 2 id, unique trong merge, ⊆ lô, **không id nào thuộc hai merge**; text non-empty sau trim. Merge vi phạm → bỏ merge đó, đếm `rejected`.
- **Guard**: lô ≥ 8 note và tổng id unique đề xuất xóa **> 50%** số note của lô → bỏ TOÀN BỘ lô (không ghi gì), đếm `skipped`, log lý do. (Lô < 8 tin model — gộp cả lô nhỏ là hợp lệ, text vẫn còn trong note gộp.) Hằng số `minGuardBatch=8` kèm comment `ponytail:` là heuristic.

### B6. Apply & lỗi

- Mỗi merge hợp lệ: `WriteNote{space, kind, text, source="consolidate", project, tags=hợp nhất tags thành viên}` → `HardDeleteNotes(members)`, **loại trừ newID** (nếu text gộp trùng note đã có, UpsertNote trả row cũ — không được xóa chính nó). Ghi trước, xóa sau (như extract hiện tại).
- Log usage (chỉ số, không nội dung): `consolidate: spaces=… partitions=… batches=… merges=… deleted=… rejected=… skipped=…`.
- Lỗi Chat/parse → trả err → FailJob (backoff sẵn có; đủ 5 lượt → dead; dead không chặn Enqueue nên tuần sau có job mới).
- Không heartbeat: quy mô cá nhân chỉ vài lô/lần — dưới xa `StaleRunning` 30'. Comment `ponytail:`: store lớn hơn thì thêm touch `updated_at` giữa các lô (handler hiện không nhận job id).
- Không CLI (`mind-runner consolidate` không tồn tại — chốt).

## C. Bỏ hẳn supersede — xóa cứng mọi nơi

Nguyên tắc: thay thế note = ghi mới + XÓA CỨNG note cũ cả chuỗi (relations → embeddings → chunks → notes; trigger dọn `chunks_fts`). Không còn status `superseded`, không giữ lịch sử trong DB.

### C1. Store

- Mới: `HardDeleteNotes(ctx, ids)` — một tx `BEGIN IMMEDIATE`: relations (`source_note_id IN ids`) → embeddings (chunk của note) → chunks → notes; `BumpGen`; COMMIT. Tách helper chung với `purgeTx` (2 caller chính đáng). Media không liên quan (note tri thức không có media).
- Purge: tập id mở rộng thêm `status='superseded'` (**mọi kind, mọi tuổi**) → quét sạch legacy một lần. `PurgeReport` + đếm riêng `Superseded` — maintenance in `superseded=%d` (nguyên tắc "xoá không im lặng").
- `UpsertNote`: `ON CONFLICT DO UPDATE` thêm hồi sinh `status='active', superseded_by=NULL` — ghi lại y hệt một row superseded (legacy) không bị mất nội dung trong cửa sổ trước khi purge quét.
- Xóa `SupersedeNote`. Cột `status`/`superseded_by` + field `Note.Status/SupersededBy` + scan **giữ nguyên** (không migration); query giữ `WHERE status='active'` làm lưới an toàn tới khi purge quét hết (sau đó trơ nhưng rẻ). `migrations/*.sql` không sửa (là lịch sử).

### C2. Recall

- Bỏ `IncludeSuperseded` khỏi: `RecallIn` (schema), `RecallParams`, 2 chỗ dựng `NoteFilter` trong `brain/recall.go`, field trong `NoteFilter` (`search_fts.go` — điều kiện lọc `status='active'` thành luôn áp).
- Bỏ `Status`/`SupersededBy` khỏi `HitOut` + `RecallHit` + chỗ dựng hit. Stages không đổi.

### C3. remember (tool)

- Param `supersedes` **giữ tên** (đường sửa ký ức + "mind fix"); ngữ nghĩa mới: sau khi ghi note mới → xóa cứng note cũ. Validation cũ giữ (tồn tại + cùng space). Guard `old != newID` giữ.
- `RememberOut.Superseded` giữ field, đổi nghĩa = id note cũ ĐÃ XÓA. Desc + jsonschema: "the old one is permanently deleted (recovery only via backups)".
- `forget` desc (`tools.go` ~637) bỏ "(keeps history)".
- `server.go` Instructions: bullet decision (supersedes) + dòng "mind fix" nói rõ note cũ bị xoá hẳn.

### C4. Extract dedupe

- `dedupe.go`: `plan.supersede` → `plan.replaces`; `supersedeThreshold` → `replaceThreshold`, `supersedeCandidates` → `replaceCandidates`; comment đầu file viết lại ("note cũ bị xóa cứng").
- `extract.go`: sau `WriteNote`, nếu `plan.replaces[i]` khác 0 và khác `res.NoteID` → `HardDeleteNotes(ctx, []int64{old})`; `stat.superseded` → `stat.deleted`; log đổi key.

### C5. Briefing

- Bỏ nhánh marker `(đã bị #%d thay)` (`briefing.go` ~355).

### C6. Docs

- README: bảng kind (procedure), câu recall về "ẩn note đã superseded" (~70), bảng meta supersedes (~49), config `[consolidate]`.

## D. Non-goals

- Không UI skill/procedural; không CLI consolidate.
- Không migration / không drop cột `status`/`superseded_by`.
- Không đổi `forget` (soft + ân hạn), ngưỡng dedupe (0.92/0.95), `ingestKinds`.
- Không merge xuyên batch/partition; không heartbeat job; không rollback cục bộ khi job chết giữa chừng (merge đã apply vẫn hợp lệ — retry tính lại từ trạng thái mới).

## E. Kiểm thử

- parse: kind `procedure` hợp lệ (extract_test).
- store: `HardDeleteNotes` xóa đủ chuỗi + FTS hết hit; purge quét row superseded legacy dù kind "vĩnh viễn" + `PurgeReport.Superseded`; `UpsertNote` hồi sinh row superseded.
- consolidate: validation (id lạ/cross-kind/trùng merge), guard skip lô ≥ 8, apply gọi WriteNote + HardDeleteNotes (egress stub như extract_test).
- server: `remember supersedes` → note cũ không còn (FetchNote ErrNoteNotFound); recall không còn `include_superseded` (rewrite `structured_test.go`).
- dedupe_test: `plan.replaces`; accuracy_test: sau re-extract note cũ biến mất.
- Chạy `go test ./...`; tuỳ chọn thêm fixture procedure vào eval extract (không chặn).

## F. Thứ tự thực hiện

1. Kind `procedure` (nhỏ, độc lập) → verify: parse test + go test.
2. `HardDeleteNotes` + purge sweep + bỏ supersede toàn bộ (store → recall → tools → briefing → extract) + sửa/xóa test liên quan → verify: go test ./... .
3. Job `consolidate` (config → enqueue maintenance → package + prompt + handler + tests) → verify: unit test guard/validation + maintenance chạy tay in dòng `consolidate:`.
4. Docs (README) + build + chạy maintenance thử trên DB thật (kiểm tra backup tồn tại trước) → verify: counters in ra, `go vet`/test xanh.
