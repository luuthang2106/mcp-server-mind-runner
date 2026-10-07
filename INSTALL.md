# Cài đặt mind-runner (macOS)

Tài liệu này viết để **dán nguyên văn cho agent Claude Code của bạn** — agent tự chạy các bước rồi báo kết quả. Tự làm tay cũng theo đúng thứ tự này.

## 0. Chuẩn bị

- macOS (Apple Silicon hoặc Intel). Chỉ hỗ trợ macOS.
- **Một gateway OpenAI-compatible**: URL (ví dụ `https://gateway.example.com/v1`) + API key, có các model:
  embed (`/embeddings`), rerank (`/rerank`), chat cho extract (`/chat/completions`), omni (audio/ảnh/video — tuỳ chọn).
  Mặc định embed `text-embedding-v4`, rerank `qwen3-rerank`; đổi được trong config.
- Claude Desktop và/hoặc Claude Code đã cài.
- Tùy chọn: Ollama đang chạy nếu muốn một phần dữ liệu chỉ dùng model local (nâng cao: `[spaces.match]` + policy `local`).

## 1. Tải

Mở [https://github.com/luuthang2106/mcp-server-mind-runner/releases/latest](https://github.com/luuthang2106/mcp-server-mind-runner/releases/latest), tải 2 file:

- `mind-runner-<version>.mcpb` — cho Claude Desktop.
- `mind-runner_<version>_darwin_arm64.zip` (máy Intel: `..._amd64.zip`) — CLI.

Giải nén vào thư mục cố định, ví dụ `~/mind-runner`:

```bash
mkdir -p ~/mind-runner && unzip -o ~/Downloads/mind-runner_*_darwin_*.zip -d ~/mind-runner
```

## 2. Gỡ quarantine

File tải từ mạng bị macOS đánh dấu cách ly; binary chỉ được ad-hoc codesign (chưa có Apple Developer ID) nên cần gỡ:

```bash
xattr -d com.apple.quarantine ~/mind-runner/mind-runner 2>/dev/null || true
```

Biến thể khi giải nén cả thư mục (zip có thêm README — file trong thư mục vẫn dính cờ):

```bash
xattr -cr ~/mind-runner
```

Nếu vẫn bị chặn: System Settings → Privacy & Security → "Open Anyway".

## 3. Setup

> **API key không nằm trong `config.toml`.** Key chỉ được khai báo trong setting MCP của từng client
> (env `MIND_RUNNER_GATEWAY_API_KEY`) — Claude Code do setup ghi giúp, Claude Desktop nhập trong hộp cấu hình extension.

Claude Code (khuyên dùng — đăng ký MCP server kèm key):

```bash
~/mind-runner/mind-runner setup --claude-code --non-interactive \
  --gateway-url https://gateway.example.com/v1 --gateway-key <key> \
  --extract-model <model-chat> --omni-model <model-omni>
# tuỳ chọn: --embed-model <model> --rerank-model <model>
```

Endpoint và tên model nằm trong `~/.config/mind-runner/config.toml` (`[gateway].base_url`, `[gateway.models]`) — sửa tay rồi mở lại client cũng được.
Bỏ `--non-interactive` để setup hỏi từng mục.

Setup sẽ: ghi config `~/.config/mind-runner/config.toml` (0600, không chứa key), khởi tạo DB, cài launchd maintenance 3:30 hằng ngày,
và chạy `claude mcp add-json -s user mind-runner '{"command":…,"env":{"MIND_RUNNER_GATEWAY_API_KEY":…}}'`.
Mặc định **không cài hook**: agent tự quyết khi nào gọi `briefing`/`remember`/`recall`/`task_add` theo MCP instructions. Hook mind-runner cũ trong `~/.claude/settings.json` (nếu có) bị gỡ, hook khác giữ nguyên (backup `settings.json.bak-<thời gian>`).
Muốn capture tự động bằng hook (recap chèn sẵn + ghi transcript mỗi lượt): thêm `--hooks`. Chạy lại setup an toàn: MCP cũ được thay chứ không nhân đôi.

Qoder / ZCode / agent khác: chỉ cần đăng ký MCP server stdio kèm env (xem mục 5), không cần hook.

Chỉ cấu hình chung (không đụng client): `~/mind-runner/mind-runner setup` (tương tác) hoặc `setup --non-interactive`.

Config cũ còn `[gateway].api_key`? Chạy lại setup — dòng đó bị gỡ tự động (doctor sẽ cảnh báo tới khi gỡ).

## 4. Kiểm tra

```bash
~/mind-runner/mind-runner doctor    # phải exit 0, không dòng "fail"
~/mind-runner/mind-runner status    # schema version, sizes, job lỗi
```

`doctor` chạy từ terminal sẽ báo `info: shell này không có MIND_RUNNER_GATEWAY_API_KEY` — bình thường, vì key nằm trong setting MCP.
Job cần cloud (embed/extract/omni) do MCP server xử lý (quét 5 phút/lần khi client đang mở); launchd maintenance không có key chỉ dọn dẹp/backup và để job cloud chờ.

## 5. Claude Desktop & client khác

1. Claude Desktop: Settings → Extensions → kéo `mind-runner-<version>.mcpb` vào cửa sổ → nhập **Gateway URL** và **Gateway API key** (và tên model nếu chưa có `config.toml`) trong hộp cấu hình. Ô để trống = dùng giá trị trong `config.toml`.
2. **Không cần dán custom instructions**: server gửi hướng dẫn dùng tool (gọi `briefing` đầu cuộc trò chuyện, `remember`, `recall`…) qua trường `instructions` của MCP — Claude Code, Claude Desktop và các agent hỗ trợ MCP instructions tự nạp.
3. Qoder, ZCode, Cursor, Codex, …: thêm server stdio vào config MCP của app (cần đường dẫn tuyệt đối; khoá ngoài cùng tuỳ app — Qoder `~/.qoder/settings.json` → `mcpServers.mind-runner`, ZCode `~/.zcode/cli/config.json` → `mcp.servers.mind-runner` thêm `"type": "stdio"`):

```json
{"command": "/Users/<you>/mind-runner/mind-runner", "args": ["mcp"],
 "env": {"MIND_RUNNER_GATEWAY_BASE_URL": "https://gateway.example.com/v1",
         "MIND_RUNNER_GATEWAY_API_KEY": "<key>",
         "MIND_RUNNER_MODEL_EXTRACT": "<model-chat>"}}
```

## Recap đầu ngày

Briefing (việc đang mở, quyết định, sở thích, phiên gần đây) được trả **1 lần mỗi ngày làm việc** (việc của project đang mở lên đầu):

- Mặc định: model gọi tool `briefing` ở câu hỏi đầu của cuộc trò chuyện mới theo instructions; `delivered=false` nghĩa là hôm nay đã brief ở cuộc khác.
- Claude Code cài với `--hooks`: hook `SessionStart` + `UserPromptSubmit` chèn sẵn recap, không phụ thuộc model.
- Ngày làm việc bắt đầu lúc `[briefing].day_start_hour` (mặc định `4` — làm khuya qua nửa đêm vẫn tính là hôm trước):

```toml
[briefing]
day_start_hour = 4
token_budget = 1500
```

## Cập nhật lên bản mới

Dữ liệu không mất. Schema DB tự nâng cấp ngay lần đầu binary mới mở DB (MCP server, hook, maintenance hay bất kỳ lệnh nào), và trước khi nâng cấp luôn có bản sao lưu `backups/pre-migrate-<thời gian>.db`. Key giữ nguyên.

### Có dùng Claude Desktop (cách nhanh)

1. Tải file `mind-runner-<version>.mcpb` mới → Claude Desktop → Settings → Extensions → kéo vào (kiểm tra hộp **Gateway API key** vẫn còn).
2. Khi Desktop khởi động extension, binary trong `.mcpb` tự cập nhật luôn bản cài cho Claude Code (đường dẫn đã đăng ký trong `~/.claude.json`, mặc định `~/.local/bin/mind-runner`) nếu bản đó cũ hơn. Không bao giờ hạ cấp, không tự cài mới nếu máy chưa cài cho Claude Code. Log: dòng `selfsync` trong `logs/mcp.log`.
3. Thoát rồi mở lại Claude Code (phiên đang mở vẫn chạy binary cũ trong bộ nhớ).
4. Kiểm tra: `mind-runner version` ra bản mới, `mind-runner doctor` exit 0.

Cấu hình setup mới đi kèm bản mới (hiếm) thì chạy thêm `mind-runner setup --claude-code --non-interactive` — release notes sẽ ghi rõ khi cần.

### Chỉ dùng Claude Code

1. Tải zip mới. **Xoá binary cũ trước** rồi mới giải nén vào **đúng thư mục cũ**, vì MCP và launchd đều trỏ tới đường dẫn này. Ghi đè thẳng lên file đang chạy có thể làm macOS kill binary mới do cache chữ ký:

   ```bash
   rm -f ~/mind-runner/mind-runner
   unzip -o ~/Downloads/mind-runner_*_darwin_*.zip -d ~/mind-runner
   xattr -cr ~/mind-runner
   ```

2. Chạy lại setup để nhận cấu hình mới (an toàn khi chạy nhiều lần). **Không cần nhập lại key**: setup tự lấy key đang có trong setting MCP của Claude Code, hoặc key cũ trong `config.toml` rồi gỡ nó khỏi file:

   ```bash
   ~/mind-runner/mind-runner setup --claude-code --non-interactive
   ```

3. Thoát rồi mở lại Claude Code.
4. Kiểm tra: `~/mind-runner/mind-runner doctor` (exit 0) và `status` (schema version là bản mới nhất).

Quay về bản cũ: dừng các client, chép `backups/pre-migrate-*.db` đè lên `mind-runner.db` (xoá `-wal`/`-shm`, giống mục Restore trong README), rồi đặt lại binary cũ.

## 6. Lỗi thường gặp

- **Gatekeeper chặn mở binary**: làm bước 2 (quarantine).
- **Doctor báo "launchd plist chưa có"**: setup đã dùng `--skip-launchd` → chạy lại `setup` không kèm flag. Maintenance đầu tiên chạy lúc login kế tiếp, hoặc chạy tay `mind-runner maintenance`.
- **`watch_dirs` không đọc được file**: thư mục ngoài home (Desktop/Documents/…) → cấp Full Disk Access cho binary trong System Settings → Privacy & Security → Full Disk Access.
- **Space `work` policy `local`**: cần Ollama chạy sẵn; `mind-runner doctor` warn khi endpoint chết.
- **Ghi âm có người khác**: chỉ ingest khi mọi người trong bản ghi đã đồng ý (consent). Audio **không được redact** trước khi gửi omni — xem README mục Egress.

## 7. Gửi log để phân tích

Mọi lần gọi gateway (chat/embed/rerank/omni) được đo và ghi vào `~/Library/Application Support/mind-runner/logs/egress.log` (JSON mỗi dòng, rotate 5MB, giữ thêm `.1`). Log **chỉ có số đo**: bytes, token, token trúng cache, độ trễ, mã lỗi, model, mục đích (`extract_session`, `summarize_session`, `embed_chunk`, `recall`, …). Không có nội dung hội thoại, ghi chú hay key.

- `egress: bất thường` (WARN), luôn ghi: lỗi, request lớn (> 30K token hoặc > 256KB, không tính dữ liệu media), chậm (> 60s), gateway không trả `usage`; `extract: delta dài` khi một lượt phải chia > 2 cửa sổ.
- `egress: mẫu` (INFO): lần gọi đầu của mỗi model/mục đích trong mỗi process, sau đó 1/50.
- `egress: tổng hợp` (INFO), mỗi giờ và khi process thoát: số lần gọi, tổng token, `cache_hit_ratio` (`-1` = gateway không báo cache), độ trễ trung bình/max.

Gửi log:

```bash
cd ~/Library/Application\ Support/mind-runner/logs && zip -q ~/Desktop/mind-runner-logs.zip egress.log* mcp.log* hook.err.log 2>/dev/null; mind-runner version
```

Gửi file `~/Desktop/mind-runner-logs.zip` kèm dòng version.
