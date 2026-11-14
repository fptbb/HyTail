//! Build and embed the port-restricted Tailcat-based TCP/UDP helper.
//! The official Tailcat CLI's exit-node behavior is deliberately not used:
//! the helper exposes only an explicitly configured local port and protocol.
use flate2::{Compression, write::ZlibEncoder};
use std::{env, fs, io::Write, path::PathBuf, process::Command};

fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    // Watch the whole module, including portset, invitedns and future packages.
    println!("cargo:rerun-if-changed=go-helper");
    println!("cargo:rerun-if-env-changed=UPX");
    println!("cargo:rerun-if-env-changed=TAILCAT_SKIP_UPX");
    println!("cargo:rerun-if-env-changed=HYTAIL_SKIP_EMBED_HELPER");
    if let Err(err) = prepare() { panic!("Failed to embed restricted Tailcat helper: {err}"); }
}

fn prepare() -> Result<(), Box<dyn std::error::Error>> {
    let target = env::var("TARGET")?;
    let (os, arch, name) = match target.as_str() {
        "x86_64-unknown-linux-gnu" => ("linux", "amd64", "hytail-helper"),
        "aarch64-unknown-linux-gnu" => ("linux", "arm64", "hytail-helper"),
        "x86_64-pc-windows-msvc" => ("windows", "amd64", "hytail-helper.exe"),
        "aarch64-pc-windows-msvc" => ("windows", "arm64", "hytail-helper.exe"),
        _ => return Err(format!("Unsupported target {target}; Linux/Windows x64/ARM64 only").into()),
    };
    let output = PathBuf::from(env::var("OUT_DIR")?);
    // Rust unit tests exercise parsing, policy, and orchestration logic without
    // launching the network helper. Avoid compiling the large Go dependency
    // graph in the test profile; release builds always embed the real helper.
    if env::var_os("HYTAIL_SKIP_EMBED_HELPER").is_some() {
        if env::var("PROFILE")? != "debug" {
            return Err("HYTAIL_SKIP_EMBED_HELPER is only allowed for debug test builds".into());
        }
        let mut encoder = ZlibEncoder::new(Vec::new(), Compression::best());
        encoder.write_all(&[])?;
        fs::write(output.join("helper.zlib"), encoder.finish()?)?;
        println!("cargo:warning=Skipping embedded Go helper for unit tests");
        return Ok(());
    }
    let executable = output.join(name);
    let module = PathBuf::from(env::var("CARGO_MANIFEST_DIR")?).join("go-helper");
    // Go 1.27.1+ is required at build time for the pinned Tailcat v0.7.0 module.
    // The Go linker produces a standalone helper: Go is not needed to run the GUI.
    let result = Command::new("go")
        .args(["build", "-mod=mod", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o"])
        .arg(&executable).arg(".")
        .current_dir(module)
        .env("CGO_ENABLED", "0")
        .env("GOOS", os)
        .env("GOARCH", arch)
        .env("GOTOOLCHAIN", "auto")
        .status()
        .map_err(|e| format!("Cannot run Go: {e}. Install Go (with toolchain auto-download enabled)."))?;
    if !result.success() { return Err(format!("Go helper build failed: {result}").into()); }
    if fs::metadata(&executable)?.len() < 100_000 { return Err("Helper build produced an unexpectedly small executable".into()); }

    if arch == "amd64" && env::var_os("TAILCAT_SKIP_UPX").is_none() {
        let upx = env::var("UPX").unwrap_or_else(|_| "upx".into());
        let original = fs::read(&executable)?;
        let packed = Command::new(&upx).args(["--best", "--lzma"]).arg(&executable).status()
            .is_ok_and(|s| s.success())
            && Command::new(&upx).arg("-t").arg(&executable).status().is_ok_and(|s| s.success());
        if !packed {
            fs::write(&executable, original)?;
            eprintln!("UPX unavailable or unsupported; helper will be DEFLATE-embedded");
        }
    }
    let mut encoder = ZlibEncoder::new(Vec::new(), Compression::best());
    encoder.write_all(&fs::read(&executable)?)?;
    let compressed = encoder.finish()?;
    fs::write(output.join("helper.zlib"), &compressed)?;
    eprintln!("Embedded restricted Tailcat helper: {} compressed bytes for {target}", compressed.len());
    fs::remove_file(executable)?;
    Ok(())
}
