use crate::{generic::{self, GenericInvite, Protocol}, invite::{self, BridgeInvite}, join_detect::JoinPreset, ports};
use serde::Deserialize;
use serde_json::Value;
use std::{
    fs::{self, File}, io,
    net::Ipv4Addr,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{atomic::{AtomicBool, Ordering}, mpsc::Sender, Arc},
    thread,
    time::{Duration, Instant},
};

pub enum Event {
    DetectedPreset(JoinPreset),
    Ready { code: String, details: String },
    Error(String),
    Stopped,
    Stats(TunnelStats),
}

pub enum Work {
    HostHytale { original: Value, port: u16 },
    HostGeneric { name: String, preset: String, protocol: Protocol, ports: Vec<u16>, target: Ipv4Addr },
    JoinCode { input: String, preset: String, local_port: Option<Vec<u16>> },
}

pub fn spawn(work: Work, stop: Arc<AtomicBool>, tx: Sender<Event>) -> thread::JoinHandle<()> {
    thread::spawn(move || {
        let outcome = match work {
            Work::HostHytale { original, port } =>
                run_host(&[port], Protocol::Udp, "auto", HostKind::Hytale(original), &stop, &tx),
            Work::HostGeneric { name, preset, protocol, ports, target } =>
                run_host(&ports, protocol, &target.to_string(), HostKind::Generic{name,preset}, &stop, &tx),
            Work::JoinCode { input, preset, local_port } =>
                run_join_code(&input, &preset, local_port, &stop, &tx),
        };
        if let Err(err) = outcome { let _ = tx.send(Event::Error(err)); }
        let _ = tx.send(Event::Stopped);
    })
}

enum HostKind { Hytale(Value), Generic {name: String, preset: String} }
enum JoinKind { Hytale(BridgeInvite), Generic(GenericInvite) }

struct ChildProcess(Child);
impl ChildProcess {
    fn check_running(&mut self) -> Result<(), String> {
        match self.0.try_wait().map_err(|e| e.to_string())? {
            Some(status) => Err(format!("Network helper exited unexpectedly: {status}")),
            None => Ok(()),
        }
    }
}
impl Drop for ChildProcess {
    fn drop(&mut self) { let _ = self.0.kill(); let _ = self.0.wait(); }
}

fn embedded_helper(work_dir: &Path) -> Result<PathBuf, String> {
    const COMPRESSED: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/helper.zlib"));
    let filename = if cfg!(windows) { "hytail-helper.exe" } else { "hytail-helper" };
    let path = work_dir.join(filename);
    let mut file = File::create(&path).map_err(|e| e.to_string())?;
    let mut decoder = flate2::read::ZlibDecoder::new(COMPRESSED);
    io::copy(&mut decoder, &mut file).map_err(|e| format!("Embedded helper extraction failed: {e}"))?;
    file.sync_all().map_err(|e| e.to_string())?;
    #[cfg(unix)] {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).map_err(|e| e.to_string())?;
    }
    Ok(path)
}

fn new_work_dir() -> Result<tempfile::TempDir, String> {
    let base = std::env::var_os("XDG_RUNTIME_DIR")
        .map(PathBuf::from).filter(|p| p.is_dir()).unwrap_or_else(std::env::temp_dir);
    tempfile::Builder::new().prefix("hytail-").tempdir_in(base)
        .map_err(|e| format!("Cannot create private runtime directory: {e}"))
}
fn spawn_logged(helper: &Path, args: &[String], work_dir: &Path) -> Result<ChildProcess, String> {
    let log = File::create(work_dir.join("helper.log")).map_err(|e| e.to_string())?;
    let stderr = log.try_clone().map_err(|e| e.to_string())?;
    let mut cmd = Command::new(helper);
    cmd.args(args).stdin(Stdio::null()).stdout(Stdio::from(log)).stderr(Stdio::from(stderr));
    #[cfg(windows)] {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x08000000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    Ok(ChildProcess(cmd.spawn().map_err(|e| format!("Cannot launch network helper: {e}"))?))
}
fn wait_for_ready<T: serde::de::DeserializeOwned>(file: &Path, child: &mut ChildProcess, stop: &AtomicBool) -> Result<Option<T>, String> {
    let deadline = Instant::now() + Duration::from_secs(40);
    while !stop.load(Ordering::Relaxed) {
        child.check_running()?;
        if let Ok(bytes) = fs::read(file) {
            if let Ok(value) = serde_json::from_slice::<T>(&bytes) { return Ok(Some(value)); }
        }
        if Instant::now() >= deadline { return Err("Timed out connecting to Tailcat; inspect relay connectivity".into()); }
        thread::sleep(Duration::from_millis(100));
    }
    Ok(None)
}

#[derive(Deserialize)] struct HostReady { address: String, ip: String }
#[derive(Deserialize)] struct JoinReady { port: u16, ip: String, #[serde(default)] ports: Vec<u16>, #[serde(default, rename = "requestedPort")] requested_port: u16, #[serde(default, rename = "portChanged")] port_changed: bool }
#[derive(Clone, Default, Debug, Deserialize)]
#[serde(default)]
pub struct ServiceProbe {
    pub port: u16,
    pub protocol: String,
    pub status: String,
}
#[derive(Clone, Default, Debug, Deserialize)]
#[serde(default)]
pub struct TunnelStats {
    pub phase: String,
    pub last_error: String,
    pub retry_attempt: u32,
    pub retry_seconds: u32,
    pub last_connected_unix: i64,
    pub service_status: String,
    pub service_ports: Vec<ServiceProbe>,
    pub local_port_policy: String,
    pub mode: String,
    pub protocol: String,
    pub target_ip: String,
    pub remote_port: u16,
    pub remote_ports: Vec<u16>,
    pub local_ports: Vec<u16>,
    pub tunnel_tx_packets: u64,
    pub tunnel_rx_packets: u64,
    pub tunnel_tx_bytes: u64,
    pub tunnel_rx_bytes: u64,
    pub active_flows: i64,
    pub last_activity_unix: i64,
    pub uptime_seconds: i64,
    pub path: String,
    pub direct_endpoint: String,
    pub derp_region: String,
    pub latency_ms: f64,
    pub path_error: String,
}
fn emit_stats(path: &Path, tx: &Sender<Event>, last: &mut Option<Vec<u8>>) {
    let Ok(bytes) = fs::read(path) else { return; };
    if last.as_ref() == Some(&bytes) { return; }
    if let Ok(stats) = serde_json::from_slice::<TunnelStats>(&bytes) {
        *last = Some(bytes);
        let _ = tx.send(Event::Stats(stats));
    }
}
fn supervise(mut child: ChildProcess, stats_path: &Path, stop: &AtomicBool, tx: &Sender<Event>) -> Result<(), String> {
    let mut last = None;
    while !stop.load(Ordering::Relaxed) {
        child.check_running()?;
        emit_stats(stats_path, tx, &mut last);
        thread::sleep(Duration::from_millis(250));
    }
    Ok(())
}
fn parse_private_ipv4(text: &str) -> Result<Ipv4Addr, String> {
    let ip: Ipv4Addr = text.parse().map_err(|_| "Invalid private bridge IP")?;
    if !ip.is_private() { return Err("Bridge must bind to a private IPv4 interface".into()); }
    Ok(ip)
}
fn ensure_distinct_endpoint(original: &Value, ip: Ipv4Addr, port: u16) -> Result<(), String> {
    let a = ip.to_string();
    if original.get("Candidates").and_then(Value::as_array).is_some_and(|xs| xs.iter().any(|c|
        c.get("Address").and_then(Value::as_str) == Some(a.as_str())
        && c.get("Port").and_then(Value::as_u64) == Some(u64::from(port)))) {
        return Err("Bridge candidate matches the original Hytale candidate".into());
    }
    Ok(())
}

fn run_host(remote_ports: &[u16], proto: Protocol, target: &str, kind: HostKind, stop: &AtomicBool, tx: &Sender<Event>) -> Result<(), String> {
    let port = *remote_ports.first().ok_or("Empty host port list")?;
    let dir = new_work_dir()?;
    let helper = embedded_helper(dir.path())?;
    let ready_file = dir.path().join("ready.json");
    let stats_file = dir.path().join("stats.json");
    let mut child = spawn_logged(&helper, &[
        "host".into(), "--port".into(), ports::csv(remote_ports),
        "--protocol".into(), proto.as_str().into(), "--target".into(), target.into(),
        "--ready-file".into(), ready_file.to_string_lossy().into_owned(),
        "--stats-file".into(), stats_file.to_string_lossy().into_owned(),
    ], dir.path())?;
    let Some(ready) = wait_for_ready::<HostReady>(&ready_file, &mut child, stop)? else { return Ok(()); };
    if !ready.address.starts_with("tc") || ready.address.len() > 8192 { return Err("Invalid Tailcat address from helper".into()); }
    let (code, details) = match kind {
        HostKind::Hytale(original) => {
            let ip = parse_private_ipv4(&ready.ip)?;
            (invite::encode_wrapper(&original, &ready.address, port, ip)?,
                format!("{} | UDP {ip}:{port}", invite::server_name(&original)))
        }
        HostKind::Generic{name,preset} => {
            let invite = if remote_ports.len() == 1 {
                GenericInvite::new(ready.address, port, proto, name.clone(), preset)
            } else {
                GenericInvite::new_multi(ready.address, remote_ports.to_vec(), proto, name.clone(), preset)
            };
            (
                generic::encode(&invite)?,
                format!("{name} | {} | {} | ports {}", proto.as_str().to_uppercase(), ready.ip, ports::csv(remote_ports))
            )
        },
    };
    let _ = tx.send(Event::Ready { code, details });
    supervise(child, &stats_file, stop, tx)
}

fn resolve_code(input: &str) -> Result<String, String> {
    let value = input.trim();
    if value.starts_with("TCBR1:") || value.starts_with("HTBR1:") { return Ok(value.to_owned()); }
    if value.len() > 253 || value.is_empty() { return Err("Expected an invite or short DNS domain".into()); }
    // DNS lookup is handled by Go's standard resolver on a worker thread,
    // matching the Android implementation and avoiding another Rust dependency.
    let dir = new_work_dir()?;
    let helper = embedded_helper(dir.path())?;
    let mut command = Command::new(helper);
    command.arg("resolve-domain").arg(value).stdin(Stdio::null());
    #[cfg(windows)] {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x08000000); // CREATE_NO_WINDOW
    }
    let result = command.output().map_err(|e| format!("DNS lookup failed: {e}"))?;
    if !result.status.success() {
        return Err(format!("DNS invite lookup: {}", String::from_utf8_lossy(&result.stderr).trim()));
    }
    let answer = String::from_utf8(result.stdout).map_err(|_| "Invalid DNS lookup output")?;
    if answer.len() > 50_001 { return Err("DNS invitation exceeds limit".into()); }
    Ok(answer.trim().to_string())
}

fn run_join_code(input: &str, _preset: &str, local_port: Option<Vec<u16>>, stop: &AtomicBool, tx: &Sender<Event>) -> Result<(), String> {
    if stop.load(Ordering::Relaxed) { return Ok(()); }
    let code = resolve_code(input)?;
    if code.starts_with("HTBR1:") {
        // The invitation, not the previously selected tab, determines the join type.
        let wrapped = invite::decode_wrapper(&code)?;
        invite::validate_expiry(&wrapped.hytale)?;
        if !wrapped.target.is_private() && !wrapped.target.is_loopback() {
            return Err("Invalid Hytale target in invite".into());
        }
        let _ = tx.send(Event::DetectedPreset(JoinPreset::Hytale));
        let address = wrapped.address.clone();
        let chosen = ports::local_mapping(&[wrapped.port], local_port.as_deref())?;
        run_join(&address, &[wrapped.port], &chosen, Protocol::Udp, JoinKind::Hytale(wrapped), stop, tx)
    } else {
        let inv = generic::decode(&code)?;
        let _ = tx.send(Event::DetectedPreset(JoinPreset::from_metadata(&inv.preset, inv.protocol)));
        let remote = inv.remote_ports();
        let chosen = ports::local_mapping(&remote, local_port.as_deref())?;
        let address = inv.address.clone();
        run_join(&address, &remote, &chosen, inv.protocol, JoinKind::Generic(inv), stop, tx)
    }
}

fn run_join(address: &str, remote_ports: &[u16], preferred_ports: &[u16], proto: Protocol, kind: JoinKind, stop: &AtomicBool, tx: &Sender<Event>) -> Result<(), String> {
    let port = *remote_ports.first().ok_or("Empty remote port list")?;
    if preferred_ports.len() != remote_ports.len() || preferred_ports.is_empty() {
        return Err("Local port mapping must match remote port count".into());
    }
    let dir = new_work_dir()?;
    let helper = embedded_helper(dir.path())?;
    let ready_file = dir.path().join("ready.json");
    let stats_file = dir.path().join("stats.json");
    let secret_file = dir.path().join("connection.txt");
    fs::write(&secret_file, address).map_err(|e| e.to_string())?;
    #[cfg(unix)] {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&secret_file, fs::Permissions::from_mode(0o600)).map_err(|e| e.to_string())?;
    }
    let mut child = spawn_logged(&helper, &[
        "join".into(), "--port".into(), ports::csv(remote_ports), "--protocol".into(), proto.as_str().into(),
        "--addr-file".into(), secret_file.to_string_lossy().into_owned(),
        "--local-port".into(), ports::csv(preferred_ports),
        "--ready-file".into(), ready_file.to_string_lossy().into_owned(),
        "--stats-file".into(), stats_file.to_string_lossy().into_owned(),
    ], dir.path())?;
    let Some(ready) = wait_for_ready::<JoinReady>(&ready_file, &mut child, stop)? else { return Ok(()); };
    let actual = if ready.ports.is_empty() { vec![ready.port] } else { ready.ports };
    if actual.len() != remote_ports.len() || actual.iter().any(|p| *p == 0) {
        return Err("Helper returned an invalid local port mapping".into());
    }
    if ready.port != actual[0] || (ready.requested_port != 0 && ready.requested_port != port) {
        return Err("Helper reported inconsistent service ports".into());
    }
    if ready.port_changed != (actual != preferred_ports) {
        return Err("Helper reported inconsistent local port fallback status".into());
    }
    let ip = parse_private_ipv4(&ready.ip)?;
    let mappings = actual.iter().zip(remote_ports).map(|(local, remote)| {
        format!("{ip}:{local} -> Tailcat {} {remote}", proto.as_str().to_uppercase())
    }).collect::<Vec<_>>().join("; ");
    let remap_note = if actual != preferred_ports {
        format!("One or more requested local ports were busy; mapped the entire group to {}. ", ports::csv(&actual))
    } else { String::new() };
    let (code, details) = match kind {
        JoinKind::Hytale(wrapped) => {
            ensure_distinct_endpoint(&wrapped.hytale, ip, actual[0])?;
            let rewritten = invite::rewrite_local(wrapped.hytale, ip, actual[0]);
            (invite::encode_hytale(&rewritten)?, format!("{remap_note}Hytale -> {mappings}"))
        }
        JoinKind::Generic(generic) => (
            if actual.len() == 1 { format!("{ip}:{}", actual[0]) }
            else { mappings.clone() },
            format!("{remap_note}{} | {} | {} | {mappings}", generic.name, generic.protocol.as_str().to_uppercase(), generic.preset)
        ),
    };
    let _ = tx.send(Event::Ready {code, details});
    supervise(child, &stats_file, stop, tx)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test] fn private_ips_only() {
        for ip in ["192.168.1.4","10.0.2.1","172.16.0.1"] { assert!(parse_private_ipv4(ip).is_ok()); }
        for ip in ["127.0.0.1","8.8.8.8","0.0.0.0"] { assert!(parse_private_ipv4(ip).is_err()); }
    }
    #[test] fn no_collision() {
        let x = serde_json::json!({"Candidates":[{"Address":"192.168.0.2","Port":5520}]});
        assert!(ensure_distinct_endpoint(&x, Ipv4Addr::new(192,168,0,2),5520).is_err());
        assert!(ensure_distinct_endpoint(&x, Ipv4Addr::new(192,168,0,2),5521).is_ok());
    }
}
