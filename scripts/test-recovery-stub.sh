#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Runs the Go bridge recovery regressions without requiring a public Tailcat
# relay or access to a live peer. The stub is intentionally test-only.
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# Select the helper module's Go toolchain explicitly, independently of the
# container's default Go version.
GO_VERSION="$(awk '$1 == "go" { print $2; exit }' "$ROOT/go-helper/go.mod")"
[[ "$GO_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
  echo "Cannot determine Go version from go-helper/go.mod: $GO_VERSION" >&2
  exit 1
}
export GOTOOLCHAIN="go$GO_VERSION"
# Resolve/download and verify the requested toolchain with normal Go settings
# BEFORE entering offline mode. Calling the system Go wrapper with GOSUMDB=off
# during the test can still trigger the toolchain downloader, which refuses to
# verify golang.org/toolchain with checksum verification disabled.
echo "Using project Go toolchain: $GOTOOLCHAIN"
go version
GO_ROOT="$(go env GOROOT)"
GO_BIN="$GO_ROOT/bin/go"
[[ -x "$GO_BIN" ]] || {
  echo "Selected Go toolchain is missing an executable: $GO_BIN" >&2
  exit 1
}
# Invoke the selected toolchain binary so offline mode cannot select another version.
# GOTOOLCHAIN=local now means this exact binary; GOPROXY=off and GOSUMDB=off
# ensure these stub-only tests cannot fetch any modules over the network.
[[ "$(GOTOOLCHAIN=local "$GO_BIN" version)" == "go version go$GO_VERSION "* ]] || {
  echo "Selected Go executable does not match go$GO_VERSION: $GO_BIN" >&2
  exit 1
}
TEMP="$(mktemp -d)"
trap 'rm -rf -- "$TEMP"' EXIT
cp -a "$ROOT/go-helper/." "$TEMP/"
rm -f "$TEMP/go.sum"
cp -a "$ROOT/tests/recovery/tailcat-stub" "$TEMP/tailcat-stub"
cp "$ROOT/tests/recovery/desktop_reconnect_test.go" "$TEMP/reconnect_integration_test.go"
cp "$ROOT/tests/recovery/android_reconnect_test.go" "$TEMP/androidbridge/reconnect_integration_test.go"
cat > "$TEMP/go.mod" <<MOD
module example.com/hytail-helper

go $GO_VERSION

require github.com/tailscale/tailcat v0.0.0

replace github.com/tailscale/tailcat => ./tailcat-stub
MOD
printf 'Running offline Tailcat recovery tests (race detector enabled)...\n'
(cd "$TEMP" && GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off "$GO_BIN" test -race ./... && GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off "$GO_BIN" vet ./...)
