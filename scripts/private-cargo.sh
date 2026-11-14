#!/usr/bin/env bash
# Build with stable Rust source-path remapping, including dependencies.
# Usage: bash scripts/private-cargo.sh build --release [--target ...]
#        bash scripts/private-cargo.sh xwin build --release ...
set -euo pipefail

if [[ $# -lt 1 ]]; then
    echo 'Usage: private-cargo.sh <cargo arguments>' >&2
    exit 2
fi

# Cargo uses CARGO_ENCODED_RUSTFLAGS (ASCII unit separator). This handles
# spaces/Windows paths without shell-escaping source names into Rust flags.
flags=()
if [[ -n "${CARGO_ENCODED_RUSTFLAGS:-}" ]]; then
    IFS=$'\x1f' read -r -a flags <<< "${CARGO_ENCODED_RUSTFLAGS}"
elif [[ -n "${RUSTFLAGS:-}" ]]; then
    # Existing conventional flags, normally a space-delimited list. For
    # complex quoted flags, the caller should use CARGO_ENCODED_RUSTFLAGS.
    read -r -a flags <<< "${RUSTFLAGS}"
fi

add_path() {
    local source="${1:-}" destination="${2:-}"
    if [[ -n "$source" && "$source" != / && "$source" != . ]]; then
        flags+=("--remap-path-prefix=$source=$destination")
    fi
}

# Map broad home roots first, specific project/toolchain locations last;
# rustc uses the last matching prefix. Source names from dependency `file!()`
# and panic/error locations are remapped before inclusion in final binaries.
add_path "${HOME:-}" /hytail/home
add_path "${USERPROFILE:-}" /hytail/home
if [[ -n "${USERPROFILE:-}" ]]; then
    add_path "${USERPROFILE//\\//}" /hytail/home
fi
add_path "${CARGO_HOME:-}" /hytail/cargo
add_path "${RUSTUP_HOME:-}" /hytail/rustup
add_path "${GITHUB_WORKSPACE:-}" /hytail/workspace
add_path "${PWD}" /hytail/workspace
# Fedora Atomic's /var/home/<user> can differ from /home/<user> in file!().
if command -v realpath >/dev/null 2>&1; then
    add_path "$(realpath -m "${HOME:-/nonexistent}" 2>/dev/null || true)" /hytail/home
    add_path "$(realpath -m "${PWD}" 2>/dev/null || true)" /hytail/workspace
fi
# Convert between the most common Fedora Atomic aliases; neither alias
# should embed the builder's username in runtime diagnostics.
if [[ "${HOME:-}" == /var/home/* ]]; then
    add_path "/home/${HOME#/var/home/}" /hytail/home
elif [[ "${HOME:-}" == /home/* ]]; then
    add_path "/var/home/${HOME#/home/}" /hytail/home
fi

encoded=''
for flag in "${flags[@]}"; do
    if [[ -n "$encoded" ]]; then encoded+=$'\x1f'; fi
    encoded+="$flag"
done
export CARGO_ENCODED_RUSTFLAGS="$encoded"
unset RUSTFLAGS
exec cargo "$@"
