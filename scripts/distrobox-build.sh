#!/usr/bin/env bash
# Build all HyTail targets in ONE x86_64 Ubuntu 24.04 Distrobox.
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

[[ "$(uname -m)" == x86_64 ]] || { echo 'The unified builder requires an x86_64 host/container.' >&2; exit 1; }
[[ -f /etc/os-release ]] || exit 1
. /etc/os-release
[[ "${ID:-}" == ubuntu ]] || {
  echo "The unified builder uses Ubuntu 24.04 (found ${ID:-unknown})." >&2
  echo 'Point HYTAIL_BOX at the Ubuntu Distrobox, or create one with a new name.' >&2
  exit 1
}

DATA_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}"
DATA_HOME="$DATA_ROOT/hytail"
LEGACY_DATA_HOME="$DATA_ROOT/hytailscale"
if [[ ! -d "$DATA_HOME" && -d "$LEGACY_DATA_HOME" ]]; then DATA_HOME="$LEGACY_DATA_HOME"; fi
if [[ ! -d "$DATA_HOME/unified-toolchain" && -d "$LEGACY_DATA_HOME/unified-toolchain" ]]; then
  TOOL_HOME="$LEGACY_DATA_HOME/unified-toolchain"
else
  TOOL_HOME="$DATA_HOME/unified-toolchain"
fi
export CARGO_HOME="$TOOL_HOME/cargo"
export RUSTUP_HOME="$TOOL_HOME/rustup"
export CARGO_TARGET_DIR="$ROOT/target/unified"
export GOPATH="${GOPATH:-$HOME/go}"  # Reuse existing Android gomobile downloads.
export GOTOOLCHAIN=auto
export PATH="$CARGO_HOME/bin:$GOPATH/bin:$PATH"
ZIG_VERSION=0.14.1
# Rust 1.98+ adds a Cortex-A53 linker switch that older cargo-zigbuild
# versions pass to Zig, which rejects it. Version 0.23.0+ filters the switch.
ZIGBUILD_MIN_VERSION=0.23.0
ZIGBUILD_INSTALL_VERSION=0.23.4
ZIG_SHA256=24aeeec8af16c381934a6cd7d95c807a8cb2cf7df9fa40d359aa884195c4716c
UPX_VERSION=5.3.0
UPX_SHA256=79cea8a50b6fa102d10c7b79a419cd77dead3cc2d78661de2dc5e5815dc505c1

fail() { echo "ERROR: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "Missing $1. Run: just setup"; }

install_zig() {
  local prefix="$TOOL_HOME/zig-$ZIG_VERSION"
  if [[ ! -x "$prefix/zig" ]]; then
    local temp file
    temp="$(mktemp -d)"
    file="zig-x86_64-linux-$ZIG_VERSION.tar.xz"
    curl -fLsS --retry 3 "https://ziglang.org/download/$ZIG_VERSION/$file" -o "$temp/$file"
    (cd "$temp" && printf '%s  %s\n' "$ZIG_SHA256" "$file" | sha256sum -c -)
    tar -xJf "$temp/$file" -C "$TOOL_HOME"
    mv "$TOOL_HOME/zig-x86_64-linux-$ZIG_VERSION" "$prefix"
    rm -rf -- "$temp"
  fi
  mkdir -p "$CARGO_HOME/bin"
  ln -sfn "$prefix/zig" "$CARGO_HOME/bin/zig"
}

install_upx() {
  if [[ -x "$CARGO_HOME/bin/upx" ]] && "$CARGO_HOME/bin/upx" --version | grep -q '^upx 5.3.0'; then return; fi
  local temp file
  temp="$(mktemp -d)"
  file="upx-$UPX_VERSION-amd64_linux.tar.xz"
  curl -fLsS --retry 3 "https://github.com/upx/upx/releases/download/v$UPX_VERSION/$file" -o "$temp/$file"
  (cd "$temp" && printf '%s  %s\n' "$UPX_SHA256" "$file" | sha256sum -c -)
  tar -xJf "$temp/$file" -C "$temp"
  install -m 0755 "$temp/upx-$UPX_VERSION-amd64_linux/upx" "$CARGO_HOME/bin/upx"
  rm -rf -- "$temp"
}

ensure_zigbuild() {
  local installed_version=''
  if command -v cargo-zigbuild >/dev/null 2>&1; then
    installed_version="$(cargo-zigbuild --version 2>/dev/null | sed -nE 's/^cargo-zigbuild ([0-9]+\.[0-9]+\.[0-9]+).*$/\1/p' | head -n 1)"
  fi
  if [[ -n "$installed_version" ]] && dpkg --compare-versions "$installed_version" ge "$ZIGBUILD_MIN_VERSION"; then
    return 0
  fi
  echo "Updating cargo-zigbuild from ${installed_version:-missing} to $ZIGBUILD_INSTALL_VERSION (required for Rust ARM64 linking)..."
  cargo install --locked --force --version "$ZIGBUILD_INSTALL_VERSION" cargo-zigbuild
  installed_version="$(cargo-zigbuild --version 2>/dev/null | sed -nE 's/^cargo-zigbuild ([0-9]+\.[0-9]+\.[0-9]+).*$/\1/p' | head -n 1)"
  [[ -n "$installed_version" ]] && dpkg --compare-versions "$installed_version" ge "$ZIGBUILD_MIN_VERSION" \
    || fail "cargo-zigbuild >= $ZIGBUILD_MIN_VERSION is required; found ${installed_version:-unknown}"
}

setup_desktop() {
  mkdir -p "$TOOL_HOME" "$CARGO_HOME/bin" "$RUSTUP_HOME"
  if [[ -f "$TOOL_HOME/.desktop-ready-v1" ]] && command -v cargo >/dev/null && command -v cargo-xwin >/dev/null && command -v zig >/dev/null && command -v upx >/dev/null && command -v go >/dev/null; then
    # Existing Distroboxes may have the incompatible cargo-zigbuild 0.22.x.
    # Upgrade it in-place, without rerunning apt or reinstalling toolchains.
    ensure_zigbuild
    return
  fi
  echo 'Installing Linux/Windows cross-build dependencies in the shared Ubuntu Distrobox...'
  sudo apt-get update
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates curl wget git unzip zip xz-utils tar python3 file \
    build-essential pkg-config cmake ninja-build clang lld libclang-dev \
    libx11-dev libxcursor-dev libxi-dev libxrandr-dev libxinerama-dev \
    libxkbcommon-dev libxkbcommon-x11-dev libwayland-dev libgl1-mesa-dev \
    libegl1-mesa-dev libssl-dev golang-go
  if [[ ! -x "$CARGO_HOME/bin/rustup" ]]; then
    # Official rustup installer. Scoped to this private build toolchain.
    curl --proto '=https' --tlsv1.2 -fsSL https://sh.rustup.rs \
      | sh -s -- -y --no-modify-path --profile minimal --default-toolchain stable
  fi
  rustup toolchain install stable --profile minimal
  rustup target add x86_64-unknown-linux-gnu aarch64-unknown-linux-gnu \
    x86_64-pc-windows-msvc aarch64-pc-windows-msvc
  if ! command -v cargo-xwin >/dev/null 2>&1; then cargo install --locked cargo-xwin; fi
  ensure_zigbuild
  install_zig
  install_upx
  need go
  need rustc
  need cargo
  need zig
  touch "$TOOL_HOME/.desktop-ready-v1"
  echo "Toolchains installed: $(rustc --version); $(go version); $(zig version)"
}

setup_android() {
  # Works in this exact same Ubuntu box: SDK, Gradle and gomobile use shared home caches.
  bash scripts/android-build.sh setup
}

pack_executable() {
  local file="$1" backup="$1.uncompressed"
  if ! command -v upx >/dev/null 2>&1; then return; fi
  cp "$file" "$backup"
  if upx --best --lzma "$file" && upx -t "$file"; then
    rm -f "$backup"
  else
    mv -f "$backup" "$file"
    echo "UPX unsupported for $(basename "$file"); kept uncompressed binary." >&2
  fi
}

update_checksums() {
  mkdir -p dist
  (cd dist && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%f\n' | LC_ALL=C sort | while IFS= read -r file; do sha256sum -- "$file"; done > SHA256SUMS)
}

build_target() {
  local platform="$1" triple bin artifact
  case "$platform" in
    linux-x64)
      triple=x86_64-unknown-linux-gnu; bin=hytail; artifact=hytail-linux-x64
      bash scripts/private-cargo.sh build --release --target "$triple"
      ;;
    linux-arm64)
      triple=aarch64-unknown-linux-gnu; bin=hytail; artifact=hytail-linux-arm64
      # Zig supplies the cross glibc sysroot and linker; no QEMU/ARM Distrobox.
      bash scripts/private-cargo.sh zigbuild --release --target "$triple"
      ;;
    windows-x64)
      triple=x86_64-pc-windows-msvc; bin=hytail.exe; artifact=hytail-windows-x64.exe
      bash scripts/private-cargo.sh xwin build --release --target "$triple"
      ;;
    windows-arm64)
      triple=aarch64-pc-windows-msvc; bin=hytail.exe; artifact=hytail-windows-arm64.exe
      bash scripts/private-cargo.sh xwin build --release --target "$triple"
      ;;
    *) fail "Unknown desktop target: $platform" ;;
  esac
  local built="$CARGO_TARGET_DIR/$triple/release/$bin"
  [[ -s "$built" ]] || fail "Build did not create $built"
  mkdir -p dist
  cp "$built" "dist/$artifact"
  if [[ "$platform" == linux-* ]]; then chmod +x "dist/$artifact"; fi
  pack_executable "dist/$artifact"
  update_checksums
  echo "READY: dist/$artifact"
}

is_desktop() {
  case "$1" in linux-x64|linux-arm64|windows-x64|windows-arm64) return 0 ;; *) return 1 ;; esac
}

build() {
  local target="$1" platform
  if [[ "$target" == all ]] || is_desktop "$target"; then
    setup_desktop
    local targets=("$target")
    if [[ "$target" == all ]]; then targets=(linux-x64 linux-arm64 windows-x64 windows-arm64); fi
    for platform in "${targets[@]}"; do build_target "$platform"; done
  fi
  if [[ "$target" == all || "$target" == android ]]; then
    bash scripts/android-build.sh apk
    update_checksums
  fi
  echo 'Build outputs: dist/'
}

case "${1:-}" in
  setup) [[ $# -eq 1 ]] || exit 2; setup_desktop; setup_android ;;
  build) [[ $# -eq 2 ]] || exit 2; case "$2" in all|linux-x64|linux-arm64|windows-x64|windows-arm64|android) build "$2" ;; *) exit 2 ;; esac ;;
  run)
    # Use the same toolchain and Cargo target cache as release builds.
    # Keep the process attached so Ctrl+C and exit codes propagate.
    shift
    setup_desktop
    exec bash scripts/private-cargo.sh run -- "$@"
    ;;
  check)
    [[ $# -eq 1 ]] || exit 2
    setup_desktop
    exec bash scripts/private-cargo.sh check --all-targets
    ;;
  fmt)
    [[ $# -eq 1 ]] || exit 2
    setup_desktop
    exec cargo fmt --all
    ;;
  lint)
    [[ $# -eq 1 ]] || exit 2
    setup_desktop
    exec bash scripts/private-cargo.sh clippy --all-targets
    ;;
  test)
    [[ $# -eq 1 ]] || exit 2
    # Tests run inside the same build container.
    setup_desktop
    GO_MODULE_VERSION="$(awk '$1 == "go" { print $2; exit }' go-helper/go.mod)"
    [[ "$GO_MODULE_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail 'Invalid go directive in go-helper/go.mod'
    export GOTOOLCHAIN="go$GO_MODULE_VERSION"
    echo "Testing with $(go version) in the shared Distrobox"
    bash scripts/test-recovery-stub.sh
    # Check real Tailcat integration too, but never write go.sum or adjust
    # dependencies inside the tracked helper directory. Android builds already
    # use an isolated staging module for the same reason.
    (
      test_module="$(mktemp -d)"
      trap 'rm -rf -- "$test_module"' EXIT
      cp -a go-helper/. "$test_module/"
      cd "$test_module"
      go test -mod=mod ./...
    )
    HYTAIL_SKIP_EMBED_HELPER=1 bash scripts/private-cargo.sh test
    ;;
  *) echo 'Usage: distrobox-build.sh setup|test|run [args...]|check|fmt|lint|build {all|linux-x64|linux-arm64|windows-x64|windows-arm64|android}' >&2; exit 2 ;;
esac
