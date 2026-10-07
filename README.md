# mind-runner

MCP server bộ nhớ dài hạn local-first cho Claude trên macOS. Agent tự quyết ghi/nhớ qua MCP tool theo instructions (hook capture tuỳ chọn), brain (notes/recall/briefing/extract), media/omni (audio/ảnh/video → transcript/caption), egress chia policy cloud/local theo space.

Kiến trúc: một binary Go (không CGO) nói SQLite thuần (modernc) — MCP server chạy stdio cho Claude Desktop/Code/Qoder/ZCode/…; hooks `SessionStart/UserPromptSubmit/Stop/SessionEnd` (tuỳ chọn, `setup --claude-code --hooks`) ghi transcript và chèn recap đầu ngày; worker queue trong cùng process xử lý embed/extract/summarize/transcribe qua egress (gateway cloud hoặc endpoint local kiểu Ollama), policy chọn theo space.

## Build & test

```bash
make build   # → dist/mind-runner
make test    # unit + integration toàn repo
make vet
make lint    # golangci-lint: govet + staticcheck + unused
make eval    # eval recall thật (recall@10 / MRR) — cần gateway key + mạng, chạy tay
```

## Đóng gói

```bash
goreleaser release --snapshot --clean   # zip darwin arm64+amd64, ad-hoc codesign, checksums
make mcpb                               # dist/mind-runner-<version>.mcpb cho Claude Desktop
```

### Codesign & Gatekeeper (macOS)

- **Ad-hoc codesign**: chưa có Apple Developer ID (open question #1) — goreleaser ký ad-hoc qua build hook, đủ chạy máy cá nhân.
- **Gatekeeper/quarantine**: binary tải từ mạng bị macOS cách ly → gỡ bằng `xattr -d com.apple.quarantine <binary>` (hoặc `xattr -cr <dir>` khi giải nén cả thư mục) — chi tiết trong INSTALL.md.
- **Notarize (tương lai)**: có Dev ID thì bật config `notarize` của goreleaser; quy trình còn lại không đổi.

CI (`.github/workflows/ci.yml`): test trên ubuntu + macos, lint; riêng push `main` đóng gói snapshot và upload artifact `.mcpb` + zip. Không job nào chạy `make eval`.

## Dữ liệu

- Config: `~/.config/mind-runner/config.toml` (0600) — **không chứa API key**; key chỉ qua env `MIND_RUNNER_GATEWAY_API_KEY` trong setting MCP của client. Server gửi hướng dẫn dùng tool qua MCP `instructions`. Data dir mặc định: `~/Library/Application Support/mind-runner/`.
- `mind-runner.db` (+ `-wal`/`-shm`) — SQLite; `media/<sha[:2]>/<sha><ext>` — file gốc theo sha256; `backups/` — VACUUM INTO hằng ngày, giữ 7 bản; `logs/` (`egress.log` = số đo usage gateway, không nội dung — INSTALL mục 7); `spool/`.

### Ghi có cấu trúc

Mỗi note giữ `text` là một câu tự nhiên; các trường phụ nằm trong `notes.meta` (JSON). Tất cả đều tuỳ chọn và chỉ điền khi người dùng nói rõ, không bao giờ bịa:

| kind | trường |
|---|---|
| `decision` | `why`, `alternatives`, `who`, `when`; `supersedes` đánh dấu quyết định cũ là `superseded` |
| `fact` | `ref` (nguồn), `as_of`, `who` |
| `preference` | `scope`, `why` |
| `note` | `who`, `when`, `ref` |
| `document` (ingest) | `title`, `summary`, `ref` = đường dẫn file, tag `file:<tên>` |

Task có thêm `why`, `owner`, `waiting_on`, `due` (YYYY-MM-DD), `constraints`; briefing có mục **Quá hạn / sắp đến hạn** (trong 3 ngày) và **Đang chờ người khác**. Extractor chạy nền cũng ghi các trường này.

Đầu briefing có mục **Đã ghi kể từ recap trước**: note do agent (hoặc hook) tự ghi (không gồm tài liệu nạp tay) + task mới/đã đóng kể từ lần recap trước (tối đa 7 ngày, 12 dòng), kèm `#id` — liếc qua, sai thì bảo agent sửa/xoá theo id.

### Project tự động

Không cần chia bộ nhớ. Mọi thứ nằm trong một kho, mỗi note/task/phiên tự mang nhãn `project` = tên thư mục gốc git của cwd (worktree → repo chính; không có git → tên thư mục; home hoặc `/` → không nhãn, ví dụ Claude Desktop). MCP server chạy với cwd = thư mục dự án (Claude Code) nên nhãn có sẵn, không cấu hình gì; client không đặt cwd → không nhãn.

- **recall** tìm khắp nơi, nhưng note cùng project đang mở được đẩy lên trước khi điểm ngang nhau (không lọc mất kết quả). Tham số `project` chỉ dùng khi hỏi rõ về một project khác.
- **task_list** liệt kê mọi việc, project hiện tại lên đầu, mỗi việc có `project`; `project` để lọc, `status: all` để xem cả việc đã xong/bỏ.
- **briefing** (một lần mỗi ngày): việc của project đang mở lên đầu; quá hạn/đang chờ lấy từ mọi project; mục thuộc project khác ghi `[tên-project]`.
- Chống trùng task chỉ trong cùng project: "Viết README" ở hai repo là hai việc.

`[spaces]` vẫn còn cho người cần tách policy cloud/local (ví dụ một thư mục chỉ được dùng model local qua `[spaces.match]`), nhưng mặc định mọi thứ ở `personal` và tool không nhận tham số space.

Recall mặc định trả 5 kết quả, bỏ hit có điểm rerank dưới 0.2 và ẩn note đã superseded; lọc được theo `kinds`. Chỉnh trong config:

```toml
[recall]
limit = 5        # 1..50
min_score = 0.2  # 0 = tắt ngưỡng; chỉ áp dụng khi rerank chạy được
```

### Client được hỗ trợ

Mặc định không có hook: agent đọc MCP instructions và tự gọi `briefing` (đầu cuộc trò chuyện), `remember`/`task_add` (khi có quyết định/việc), `recall` (khi cần ngữ cảnh cũ). Tiền tố `mind:`, `mind recall:`, `mind todo:`, `mind fix:`, `mind forget:` ở đầu tin nhắn ép gọi tool tương ứng.

| Client | Cài |
|---|---|
| Claude Code | `setup --claude-code` (thêm `--hooks` để capture transcript tự động) |
| Qoder, ZCode, Cursor, … | thêm server stdio vào config MCP của app (INSTALL.md mục 5) |
| Claude Desktop | kéo `.mcpb` |

### Restore từ backup

```bash
launchctl bootout gui/$(id -u)/com.mind-runner.maintenance 2>/dev/null || true
cd ~/Library/Application\ Support/mind-runner
cp backups/mind-runner-YYYYMMDD.db mind-runner.db
rm -f mind-runner.db-wal mind-runner.db-shm
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.mind-runner.maintenance.plist
```

**Media backup = copy thường**: `media/` không nằm trong `VACUUM INTO` — muốn đủ thì copy cả thư mục (`cp -R` / rsync) sang nơi khác.

## Retention (D13)

| Dữ liệu | Mặc định | Config |
|---|---|---|
| events / episodes / relations / tasks | 365 ngày | `[retention].events_days` |
| session_raw (nội dung thô) | 90 ngày | `[retention].raw_days` |
| jobs done/dead | 30 ngày (failed còn retry thì giữ); `0` ở bất kỳ tầng nào = tắt purge tầng đó | `[retention].jobs_days` |
| backups | 7 bản | `[backup].keep` |
| media gốc | giữ vô hạn; `false` → xoá file sau transcribe, row + transcript còn (path rỗng — doctor không báo thiếu) | `[media].keep_originals` |

## Egress & riêng tư

Text gửi egress (embed/rerank/chat/omni prompt) đều qua redact built-in: private key, AWS key, `sk-…`, GitHub token, bearer, `password/secret/token=…` — log chỉ số lần khớp theo tên rule, không log nội dung. **Audio/ảnh không redact được** — file gốc gửi nguyên cho omni model (cloud hay local theo policy space). Thành thật: chỉ ingest bản ghi khi mọi người trong đó đã đồng ý (consent) — đặc biệt khi policy space là cloud.

## Ngưỡng omni đã verify thật (macOS)

| Bước | Lệnh/công cụ thật | Hằng số |
|---|---|---|
| Video → audio | `avconvert --preset PresetAppleM4A` | chỉ gửi audio đã trích (m4a), không gửi container |
| Giảm audio | `afconvert -f m4af -d aac -b <br> -c 1 -r 16000` | ladder 64k → 32k → 16k bps mono 16kHz; cạn ladder → job dead |
| Ảnh egress | `sips -s format jpeg` (HEIC) + imaging | cạnh dài ≤ 1280px, JPEG q85, strip EXIF/GPS |
| Trần 1 request omni | — | base64 ≤ 10 MiB (≈ 15 phút m4a) — vượt là dead kèm hướng dẫn cắt ngắn |

## Tài liệu

- `INSTALL.md` — hướng dẫn cài cho người dùng cuối (dán được cho agent).
