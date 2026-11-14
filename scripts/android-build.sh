#!/usr/bin/env bash
# Build a locally signed, updateable Android ARM64 RELEASE APK.
# Called by the unified Distrobox script or GitHub Actions.
set -euo pipefail
umask 077
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}"
DATA_HOME="$DATA_ROOT/hytail"
LEGACY_DATA_HOME="$DATA_ROOT/hytailscale"
select_data_dir() {
  local sub="$1"
  if [[ -e "$DATA_HOME/$sub" ]]; then printf '%s' "$DATA_HOME/$sub"
  elif [[ -e "$LEGACY_DATA_HOME/$sub" ]]; then printf '%s' "$LEGACY_DATA_HOME/$sub"
  else printf '%s' "$DATA_HOME/$sub"; fi
}
SDK="${ANDROID_HOME:-$(select_data_dir android-sdk)}"
SDK_TOOLS="$SDK/cmdline-tools/latest/bin"
NDK_VERSION="27.0.12077973"
GRADLE_VERSION="8.11.1"
GO_TOOLCHAIN="go1.27.1"
MOBILE_VERSION="v0.0.0-20261011034528-197770444867"
ANDROID_TOOLS_VERSION="15859902"
TOOLS_SHA256="4e4c464f145a7512b57d088ac6c278c03c9eea610886b35a5e0804e74eedf583"
GRADLE_HOME="$(select_data_dir "gradle-${GRADLE_VERSION}")"
SIGNING_DIR="${HYTAIL_ANDROID_SIGNING_DIR:-${HYTAILSCALE_ANDROID_SIGNING_DIR:-$(select_data_dir android-signing)}}"
# Accept old environment names and normalize to the new HyTail names.
export HYTAIL_ANDROID_KEYSTORE="${HYTAIL_ANDROID_KEYSTORE:-${HYTAILSCALE_ANDROID_KEYSTORE:-}}"
export HYTAIL_ANDROID_STORE_PASSWORD="${HYTAIL_ANDROID_STORE_PASSWORD:-${HYTAILSCALE_ANDROID_STORE_PASSWORD:-}}"
export HYTAIL_ANDROID_KEY_PASSWORD="${HYTAIL_ANDROID_KEY_PASSWORD:-${HYTAILSCALE_ANDROID_KEY_PASSWORD:-}}"
export HYTAIL_ANDROID_KEY_ALIAS="${HYTAIL_ANDROID_KEY_ALIAS:-${HYTAILSCALE_ANDROID_KEY_ALIAS:-}}"
export ANDROID_HOME="$SDK" ANDROID_SDK_ROOT="$SDK" ANDROID_NDK_HOME="$SDK/ndk/$NDK_VERSION"
export GOTOOLCHAIN="$GO_TOOLCHAIN"
export GOPATH="${GOPATH:-$HOME/go}"
export PATH="$GOPATH/bin:$SDK_TOOLS:$SDK/platform-tools:$PATH"

fail() { echo "ERROR: $*" >&2; exit 1; }

install_deps() {
  command -v apt-get >/dev/null 2>&1 || fail 'Android setup needs an Ubuntu Distrobox (apt-get).'
  # Avoid apt when the shared Distrobox has already been prepared.
  if command -v javac >/dev/null && command -v keytool >/dev/null && command -v go >/dev/null && command -v unzip >/dev/null && command -v openssl >/dev/null; then
    return
  fi
  sudo apt-get update
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates curl wget unzip zip python3 openjdk-17-jdk-headless \
    golang-go git build-essential file openssl
}

get_android_tools() {
  [[ -x "$SDK_TOOLS/sdkmanager" ]] && return
  mkdir -p "$SDK/cmdline-tools" "$ROOT/.android-build"
  local file="$ROOT/.android-build/android-tools.zip"
  curl -fL --retry 3 "https://dl.google.com/android/repository/commandlinetools-linux-${ANDROID_TOOLS_VERSION}_latest.zip" -o "$file"
  printf '%s  %s\n' "$TOOLS_SHA256" "$file" | sha256sum -c -
  local temp="$ROOT/.android-build/unpacked-tools"
  rm -rf -- "$temp"; mkdir -p "$temp"
  unzip -q "$file" -d "$temp"
  mv "$temp/cmdline-tools" "$SDK/cmdline-tools/latest"
  rm -rf -- "$temp" "$file"
}

get_gradle() {
  [[ -x "$GRADLE_HOME/bin/gradle" ]] && return
  local home="$(dirname "$GRADLE_HOME")"
  mkdir -p "$home"
  local zip="$home/gradle-${GRADLE_VERSION}-bin.zip"
  curl -fL --retry 3 "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" -o "$zip"
  curl -fL --retry 3 "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip.sha256" -o "$zip.sha256"
  (cd "$home" && printf '%s  %s\n' "$(tr -d '[:space:]' < "$zip.sha256")" "$(basename "$zip")" | sha256sum -c -)
  unzip -q "$zip" -d "$home"
  rm -f "$zip" "$zip.sha256"
}

setup() {
  install_deps
  get_android_tools
  if [[ ! -d "$SDK/platforms/android-35" || ! -d "$ANDROID_NDK_HOME" || ! -x "$SDK/build-tools/35.0.0/apksigner" ]]; then
    yes | "$SDK_TOOLS/sdkmanager" --sdk_root="$SDK" --licenses >/dev/null || true
    "$SDK_TOOLS/sdkmanager" --sdk_root="$SDK" \
      'platform-tools' 'platforms;android-35' 'build-tools;35.0.0' "ndk;$NDK_VERSION"
  fi
  get_gradle
  if ! command -v gomobile >/dev/null 2>&1 || ! command -v gobind >/dev/null 2>&1; then
    go install "golang.org/x/mobile/cmd/gomobile@${MOBILE_VERSION}"
    go install "golang.org/x/mobile/cmd/gobind@${MOBILE_VERSION}"
    gomobile init
  fi
  echo "Android SDK/NDK, Gradle, gomobile ready ($(go version))."
}

ensure_signing() {
  # Explicit signing env vars take precedence, allowing CI to use a fixed external identity.
  if [[ -n "${HYTAIL_ANDROID_KEYSTORE:-}" ]]; then
    [[ -f "$HYTAIL_ANDROID_KEYSTORE" ]] || fail 'Configured Android keystore does not exist.'
    [[ -n "${HYTAIL_ANDROID_STORE_PASSWORD:-}" && -n "${HYTAIL_ANDROID_KEY_PASSWORD:-}" && -n "${HYTAIL_ANDROID_KEY_ALIAS:-}" ]] || fail 'Missing Android signing variables.'
    return
  fi
  local store="$SIGNING_DIR/release.jks" env_file="$SIGNING_DIR/signing.env"
  if [[ -f "$store" && ! -f "$env_file" ]] || [[ -f "$env_file" && ! -f "$store" ]]; then
    fail "Incomplete Android signing identity in $SIGNING_DIR. Restore the original pair from backup."
  fi
  if [[ ! -f "$store" ]]; then
    command -v openssl >/dev/null || fail 'openssl required to generate a release signing password.'
    command -v keytool >/dev/null || fail 'JDK keytool required for release signing.'
    mkdir -p "$SIGNING_DIR"
    chmod 700 "$SIGNING_DIR"
    local pw
    pw="$(openssl rand -hex 24)"
    # Keep the long-lived signing identity out of the project checkout and Git.
    keytool -genkeypair -noprompt -keystore "$store" -storetype PKCS12 \
      -storepass "$pw" -keypass "$pw" -alias hytail \
      -keyalg RSA -keysize 3072 -validity 10000 \
      -dname 'CN=HyTail, OU=Android, O=HyTail, C=US' >/dev/null
    printf 'HYTAIL_ANDROID_STORE_PASSWORD=%s\nHYTAIL_ANDROID_KEY_PASSWORD=%s\nHYTAIL_ANDROID_KEY_ALIAS=hytail\n' "$pw" "$pw" > "$env_file"
    chmod 600 "$store" "$env_file"
    echo "Created local release signing identity at $SIGNING_DIR"
    echo 'IMPORTANT: Back up release.jks and signing.env securely; losing either prevents updating installed releases.'
  fi
  # Generated values are strictly ASCII hex and a fixed alias, not executable input.
  # The file is private and is generated solely by this script.
  local key value
  while IFS='=' read -r key value; do
    case "$key" in
      HYTAIL_ANDROID_STORE_PASSWORD|HYTAILSCALE_ANDROID_STORE_PASSWORD) export HYTAIL_ANDROID_STORE_PASSWORD="$value" ;;
      HYTAIL_ANDROID_KEY_PASSWORD|HYTAILSCALE_ANDROID_KEY_PASSWORD) export HYTAIL_ANDROID_KEY_PASSWORD="$value" ;;
      HYTAIL_ANDROID_KEY_ALIAS|HYTAILSCALE_ANDROID_KEY_ALIAS) export HYTAIL_ANDROID_KEY_ALIAS="$value" ;;
    esac
  done < "$env_file"
  export HYTAIL_ANDROID_KEYSTORE="$store"
}

build() {
  if ! command -v gomobile >/dev/null || ! command -v gobind >/dev/null || \
     [[ ! -x "$GRADLE_HOME/bin/gradle" || ! -x "$SDK_TOOLS/sdkmanager" || ! -d "$ANDROID_NDK_HOME" ]]; then
    echo 'Android prerequisites missing; provisioning the shared Distrobox...'
    setup
  fi
  [[ "$(go env GOVERSION)" == "$GO_TOOLCHAIN" ]] || fail "Expected $GO_TOOLCHAIN, got $(go env GOVERSION)"
  ensure_signing

  local temp="$ROOT/.android-build/go-helper"
  rm -rf -- "$temp"
  mkdir -p "$temp" "$ROOT/android/app/libs" "$ROOT/dist"
  # Copy the complete module so relative internal imports (such as portset) resolve.
  cp -a "$ROOT/go-helper/." "$temp/"
  (
    cd "$temp"
    go get -tool "golang.org/x/mobile/cmd/gobind@${MOBILE_VERSION}"
    go mod tidy
    gomobile bind -target=android/arm64 -androidapi=26 -javapkg=org.hytail \
      -ldflags='-s -w' -o "$ROOT/android/app/libs/hytail.aar" ./androidbridge
  )
  unzip -p "$ROOT/android/app/libs/hytail.aar" classes.jar > "$ROOT/.android-build/aar-classes.jar"
  if ! unzip -Z -1 "$ROOT/.android-build/aar-classes.jar" | grep -Fx 'org/hytail/androidbridge/Androidbridge.class' >/dev/null; then
    fail 'Unexpected gomobile Java package/class in AAR.'
  fi

  "$GRADLE_HOME/bin/gradle" --no-daemon -p "$ROOT/android" :app:assembleRelease
  local source="$ROOT/android/app/build/outputs/apk/release/app-release.apk"
  local output="$ROOT/dist/HyTail-android-arm64-release.apk"
  [[ -s "$source" ]] || fail "Gradle did not create $source"
  cp "$source" "$output"
  "$SDK/build-tools/35.0.0/apksigner" verify --verbose "$output"
  # Local builds keep the APK in dist/. CI uploads this APK as a standalone
  # artifact and publishes it beside the desktop binaries on tagged releases.
  rm -f "$output.sha256"
  echo "READY: $output (signed release APK)"
}

case "${1:-}" in
  setup) [[ $# -eq 1 ]] || exit 2; setup ;;
  apk|build) [[ $# -eq 1 ]] || exit 2; build ;;
  *) echo 'Usage: android-build.sh setup|apk' >&2; exit 2 ;;
esac
