#!/usr/bin/env bash
# Single Distrobox driver. Requires only distrobox and its container engine on the host.
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
LEGACY_BOX="hytailscale-android"
DEFAULT_BOX="hytail-android"
if [[ -n "${HYTAIL_BOX:-${HYTAILSCALE_BOX:-}}" ]]; then
  BOX="${HYTAIL_BOX:-${HYTAILSCALE_BOX}}"
else
  BOX="$DEFAULT_BOX"
  if command -v distrobox >/dev/null 2>&1 && distrobox list --no-color 2>/dev/null | awk -F '|' -v wanted="$LEGACY_BOX" 'NF>=3 { gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); if ($2 == wanted) found=1 } END { exit !found }'; then
    BOX="$LEGACY_BOX"
  fi
fi
IMAGE="${HYTAIL_IMAGE:-${HYTAILSCALE_IMAGE:-docker.io/library/ubuntu:24.04}}"

usage() { echo 'Usage: box.sh setup|build [target]|test|run [args...]|check|fmt|lint|enter|artifacts|clean' >&2; exit 2; }
valid_target() {
  case "$1" in all|linux-x64|linux-arm64|windows-x64|windows-arm64|android) ;; *) usage ;; esac
}
box_exists() {
  # Match the NAME column exactly; do not let `distrobox enter` create the wrong default image.
  distrobox list --no-color | awk -F '|' -v wanted="$BOX" 'NF>=3 { gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); if ($2 == wanted) found=1 } END { exit !found }'
}
ensure_box() {
  command -v distrobox >/dev/null 2>&1 || { echo 'Missing distrobox on host.' >&2; exit 1; }
  if ! box_exists; then
    echo "Creating single HyTail Distrobox '$BOX' ($IMAGE)..."
    distrobox create --name "$BOX" --image "$IMAGE" --yes
  fi
}
enter_and_run() {
  local line
  printf -v line 'cd %q && bash scripts/distrobox-build.sh' "$ROOT"
  printf -v line '%s %q' "$line" "${1}"
  shift
  local arg
  for arg in "$@"; do printf -v line '%s %q' "$line" "$arg"; done
  distrobox enter --name "$BOX" -- bash -lc "$line"
}
clean() {
  # Deliberately preserve HyTail and legacy HyTailscale toolchains, SDKs, and signing keys.
  local item
  for item in \
    "$ROOT/dist" "$ROOT/target" "$ROOT/.android-build" \
    "$ROOT/android/.gradle" "$ROOT/android/.cxx" "$ROOT/android/build" \
    "$ROOT/android/app/build" "$ROOT/android/app/libs/hytail.aar"; do
    if [[ -e "$item" ]]; then rm -rf -- "$item"; echo "Removed ${item#"$ROOT"/}"; fi
  done
  echo 'Project outputs removed; cached tools and release signing identity preserved.'
}
case "${1:-}" in
  setup) [[ $# -eq 1 ]] || usage; ensure_box; enter_and_run setup ;;
  build) [[ $# -le 2 ]] || usage; target="${2:-all}"; valid_target "$target"; ensure_box; enter_and_run build "$target" ;;
  test) [[ $# -eq 1 ]] || usage; ensure_box; enter_and_run test ;;
  run) shift; ensure_box; enter_and_run run "$@" ;;
  check|fmt|lint) [[ $# -eq 1 ]] || usage; ensure_box; enter_and_run "$1" ;;
  enter) [[ $# -eq 1 ]] || usage; ensure_box; distrobox enter --name "$BOX" ;;
  clean) [[ $# -eq 1 ]] || usage; clean ;;
  artifacts) [[ $# -eq 1 ]] || usage; if [[ -d "$ROOT/dist" ]]; then find "$ROOT/dist" -maxdepth 1 -type f -printf '%f\n' | sort; else echo 'No build artifacts yet.'; fi ;;
  *) usage ;;
esac
