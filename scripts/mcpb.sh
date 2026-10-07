#!/usr/bin/env bash
# Đóng gói Claude Desktop extension (.mcpb): manifest + binary darwin universal (arm64+amd64).
# Dùng: ./scripts/mcpb.sh [version]  (không truyền → git describe)
set -euo pipefail
cd "$(dirname "$0")/.."

ver="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
ver="${ver#v}"

out="dist/mcpb"
rm -rf "$out"
mkdir -p "$out/bin"

ldflags="-X mind-runner/internal/version.Version=$ver -X mind-runner/internal/version.Commit=$(git rev-parse --short HEAD)"
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
    -o "$out/mind-runner-$arch" ./cmd/mind-runner
done
# universal binary (Apple Silicon + Intel) khi có lipo; không có thì arm64
if command -v lipo >/dev/null 2>&1; then
  lipo -create -output "$out/bin/mind-runner" "$out/mind-runner-arm64" "$out/mind-runner-amd64"
else
  cp "$out/mind-runner-arm64" "$out/bin/mind-runner"
fi
rm -f "$out/mind-runner-arm64" "$out/mind-runner-amd64"

sed "s/__VERSION__/$ver/" assets/mcpb/manifest.json > "$out/manifest.json"

# ad-hoc codesign (chưa có Apple Developer ID — README mục codesign)
if command -v codesign >/dev/null 2>&1; then
  codesign --force -s - "$out/bin/mind-runner"
fi

mkdir -p dist
rm -f "dist/mind-runner-$ver.mcpb"
(cd "$out" && zip -qr "../mind-runner-$ver.mcpb" manifest.json bin)
echo "dist/mind-runner-$ver.mcpb"
