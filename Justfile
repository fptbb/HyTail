# Unified HyTail build: one Ubuntu Distrobox, all artifacts in dist/.
set shell := ["bash", "-euo", "pipefail", "-c"]

# List commands.
default:
    @just --list

# Provision or update the shared Distrobox toolchain (Linux, Windows, Android).
setup:
    bash scripts/box.sh setup

# Build everything or one target: linux-x64, linux-arm64, windows-x64, windows-arm64, android.
build target="all":
    bash scripts/box.sh build '{{target}}'

# Run Go and Rust tests inside the shared Distrobox.
test:
    bash scripts/box.sh test

# Run the uncompressed development build in the managed Distrobox.
# No arguments open the GUI; CLI arguments are forwarded unchanged.
# Examples: just run; just run -- --help; just run host terraria
[positional-arguments]
run *args='':
    bash scripts/box.sh run "$@"

# Shortcut for the development run command.
alias dev := run

# Check Rust code using the managed toolchains.
check:
    bash scripts/box.sh check

# Format Rust source inside the managed Distrobox.
fmt:
    bash scripts/box.sh fmt

# Run Clippy with the shared build environment.
lint:
    bash scripts/box.sh lint

# Open the shared build environment.
enter:
    bash scripts/box.sh enter

# Remove generated project build files; preserve toolchains, Android SDK and release signing keys.
clean:
    bash scripts/box.sh clean

# Display artifacts and checksums.
artifacts:
    bash scripts/box.sh artifacts

# Install the signed release APK (requires adb on the host).
install-android:
    adb install -r dist/HyTail-android-arm64-release.apk
