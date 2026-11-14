//! Console front end for the same bridge workers used by the egui application.
//! One executable handles GUI (no args or `gui`) and CLI commands.
//! On Windows it uses the console subsystem for reliable terminal output.
use crate::{
    bridge::{self, Event, Work},
    generic::Protocol,
    ports,
    invite,
};
use serde_json::Value;
use std::{
    collections::BTreeMap,
    io::{self, Read, Write},
    net::Ipv4Addr,
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc,
    },
    time::{Duration, Instant},
};

const HELP: &str = r#"HyTail - encrypted peer-to-peer bridges over Tailcat

USAGE
  hytail                                Launch graphical interface
  hytail gui                            Launch graphical interface explicitly
  hytail --help                         Print this help
  hytail --version                      Print version
  hytail host hytale --code -
  hytail join hytale --code HTBR1:...  # Preset auto-detected
  hytail host generic --port 8080,8081-8083 --protocol both --target 127.0.0.1 --name Web
  hytail join generic --code TCBR1:...  # Any valid preset
  hytail host minecraft --edition java --target 127.0.0.1
  hytail join minecraft --code TCBR1:...
  hytail host terraria                          # TCP / 7777
  hytail join terraria --code TCBR1:...
  hytail join --code -             # Detect preset automatically
  # All join commands accept any bridge code; the code chooses the destination.

OPTIONS
  --code TEXT        Hytale original code or HTBR1:/TCBR1: bridge code;
                     use '-' to read standard input (recommended for secrets)
  --code-file PATH   Read the code from a file instead of command-line arguments
  --port SPEC        Host ports (e.g. 25566,25567 or 25566-25568; max 32)
  --listen-port SPEC Preferred local ports: one base port or equal-size list;
                     fallback shifts all ports together when occupied
  --protocol MODE    Generic: tcp, udp, or both (default: tcp)
  --target IP        Host service destination; loopback or assigned private IP
                     (default: 127.0.0.1 for all non-Hytale presets)
  --name TEXT        Generic service name (default: My service)
  --edition EDITION  Minecraft: java or bedrock (default: java)
  --diagnostics      Print periodic tunnel traffic and path observations
  -h, --help         Display help without opening the GUI
  --version          Show application version

A joining machine binds to an available local private-IP port, first trying
its remote port, then nearby ports if occupied. Its displayed code/address
always uses the *actual* local port; the remote service port is unchanged.

Press Ctrl+C to stop the bridge. Share bridge codes only with trusted peers.
"#;

#[derive(Debug)]
struct CommandLine {
    role: String,
    preset: String,
    values: BTreeMap<String, String>,
    diagnostics: bool,
}

fn parse_options(args: &[String]) -> Result<CommandLine, String> {
    if args.is_empty() {
        return Err("Missing host or join command; use --help".into());
    }
    let role = args[0].as_str();
    if role != "host" && role != "join" {
        return Err("Expected 'host' or 'join'; use --help".into());
    }
    let mut offset = 1;
    let preset = if let Some(p) = args.get(1).filter(|p| !p.starts_with('-')) {
        offset += 1;
        p.to_lowercase()
    } else if role == "join" {
        "auto".into()
    } else {
        return Err("Host requires a preset: hytale, generic, minecraft, or terraria".into());
    };
    if !["auto", "hytale", "generic", "minecraft", "terraria"].contains(&preset.as_str())
        || (role == "host" && preset == "auto")
    {
        return Err(format!("Unknown preset: {preset}"));
    }
    let mut values = BTreeMap::new();
    let mut diagnostics = false;
    let mut iter = args[offset..].iter();
    while let Some(arg) = iter.next() {
        if arg == "--diagnostics" {
            if diagnostics { return Err("Repeated --diagnostics".into()); }
            diagnostics = true;
            continue;
        }
        if !["--code", "--code-file", "--port", "--listen-port", "--protocol", "--target", "--name", "--edition"].contains(&arg.as_str()) {
            return Err(format!("Unknown option: {arg}"));
        }
        let val = iter.next().ok_or_else(|| format!("Missing value for {arg}"))?;
        if val.starts_with("--") { return Err(format!("Missing value for {arg}")); }
        if values.insert(arg.clone(), val.clone()).is_some() {
            return Err(format!("Repeated option: {arg}"));
        }
    }
    if values.contains_key("--code") && values.contains_key("--code-file") {
        return Err("Choose only one of --code or --code-file".into());
    }
    Ok(CommandLine { role: role.into(), preset, values, diagnostics })
}

impl CommandLine {
    fn get(&self, key: &str) -> Option<&str> { self.values.get(key).map(String::as_str) }
    fn value(&self, key: &str, default: &str) -> String {
        self.get(key).unwrap_or(default).to_string()
    }
    fn reject_except(&self, allowed: &[&str]) -> Result<(), String> {
        for key in self.values.keys() {
            if !allowed.contains(&key.as_str()) {
                return Err(format!("Option {key} is not supported for {} {}", self.role, self.preset));
            }
        }
        Ok(())
    }
    fn listen_port(&self) -> Result<Option<Vec<u16>>, String> {
        self.get("--listen-port").map(ports::parse).transpose()
    }
    fn input_code(&self) -> Result<String, String> {
        let input = if let Some(path) = self.get("--code-file") {
            let mut data = String::new();
            std::fs::File::open(path).map_err(|e| format!("Cannot open {path}: {e}"))?
                .take(512_001).read_to_string(&mut data)
                .map_err(|e| format!("Cannot read {path}: {e}"))?;
            data
        } else if self.get("--code") == Some("-") {
            // Bounded stdin so malformed input cannot exhaust memory.
            let mut data = String::new();
            io::stdin().take(512_001).read_to_string(&mut data)
                .map_err(|e| format!("Cannot read stdin: {e}"))?;
            data
        } else {
            self.get("--code").ok_or("Supply --code TEXT, --code -, or --code-file PATH")?.to_owned()
        };
        if input.len() > 512_000 { return Err("Code is too long".into()); }
        if input.trim().is_empty() { return Err("Empty code".into()); }
        Ok(input.trim().to_owned())
    }
}

fn protocol(s: &str) -> Result<Protocol, String> {
    match s {
        "tcp" => Ok(Protocol::Tcp),
        "udp" => Ok(Protocol::Udp),
        "both" => Ok(Protocol::Both),
        _ => Err("Protocol must be tcp, udp, or both".into()),
    }
}

fn prepare(cli: &CommandLine) -> Result<Work, String> {
    match (cli.role.as_str(), cli.preset.as_str()) {
        ("host", "hytale") => {
            cli.reject_except(&["--code", "--code-file"])?;
            let original: Value = invite::decode_hytale(&cli.input_code()?)?;
            invite::validate_expiry(&original)?;
            let port = invite::choose_port(&original)?;
            Ok(Work::HostHytale { original, port })
        }
        ("join", "hytale") => {
            cli.reject_except(&["--code", "--code-file", "--listen-port"])?;
            Ok(Work::JoinCode { input: cli.input_code()?, preset: "hytale".into(), local_port: cli.listen_port()? })
        }
        ("host", "generic") => {
            cli.reject_except(&["--name", "--protocol", "--port", "--target"])?;
            let ports = ports::parse(cli.get("--port").ok_or("Generic host requires --port")?)?;
            let protocol = protocol(cli.get("--protocol").unwrap_or("tcp"))?;
            let name = cli.value("--name", "My service");
            if name.trim().is_empty() || name.len() > 120 { return Err("Name must be 1-120 characters".into()); }
            let target: Ipv4Addr = cli.value("--target", "127.0.0.1").parse()
                .map_err(|_| "Target must be an IPv4 address".to_string())?;
            Ok(Work::HostGeneric { name, preset: "generic".into(), protocol, ports, target })
        }
        ("host", "minecraft") => {
            cli.reject_except(&["--edition", "--port", "--target"])?;
            let edition = cli.get("--edition").unwrap_or("java");
            let (default_port, protocol) = match edition {
                "java" => (25565, Protocol::Tcp),
                "bedrock" => (19132, Protocol::Both),
                _ => return Err("Edition must be java or bedrock".into()),
            };
            let ports = if let Some(p) = cli.get("--port") {ports::parse(p)?} else {vec![default_port]};
            let target: Ipv4Addr = cli.value("--target", "127.0.0.1").parse()
                .map_err(|_| "Target must be an IPv4 address".to_string())?;
            Ok(Work::HostGeneric { name: format!("Minecraft {edition}"), preset: "minecraft".into(), protocol, ports, target })
        }
        ("host", "terraria") => {
            cli.reject_except(&["--port", "--target"])?;
            let ports = if let Some(p) = cli.get("--port") { ports::parse(p)? } else { vec![7777] };
            let target: Ipv4Addr = cli.value("--target", "127.0.0.1").parse()
                .map_err(|_| "Target must be an IPv4 address".to_string())?;
            Ok(Work::HostGeneric {
                name: "Terraria".into(), preset: "terraria".into(), protocol: Protocol::Tcp, ports, target,
            })
        }
        ("join", "generic" | "minecraft" | "terraria" | "auto") => {
            cli.reject_except(&["--code", "--code-file", "--listen-port"])?;
            Ok(Work::JoinCode { input: cli.input_code()?, preset: cli.preset.clone(), local_port: cli.listen_port()? })
        }
        _ => Err("Unsupported command; use --help".into()),
    }
}

pub fn run_from(args: Vec<String>) -> Result<(), String> {
    if args.len() == 1 && (args[0] == "--version" || args[0] == "-V") {
        println!("HyTail {}", env!("CARGO_PKG_VERSION"));
        return Ok(());
    }
    if args.is_empty() || args.iter().any(|x| ["help", "-h", "--help"].contains(&x.as_str())) {
        println!("{HELP}");
        return Ok(());
    }
    let cli = parse_options(&args)?;
    let work = prepare(&cli)?;
    let stop = Arc::new(AtomicBool::new(false));
    let handler_stop = Arc::clone(&stop);
    ctrlc::set_handler(move || handler_stop.store(true, Ordering::SeqCst))
        .map_err(|e| format!("Cannot register Ctrl+C handler: {e}"))?;
    let (tx, rx) = mpsc::channel();
    let worker = bridge::spawn(work, Arc::clone(&stop), tx);
    eprintln!("Starting HyTail bridge... (Ctrl+C to stop)");
    let mut last_stats = Instant::now();
    let mut last_method = String::new();
    let mut last_phase = String::new();
    let mut failure = None;
    for event in rx {
        match event {
            Event::DetectedPreset(preset) => eprintln!("Detected bridge: {}", preset.as_str()),
            Event::Ready { code, details } => {
                // Print the complete code on a separate line so scripts can
                // capture it without accidentally including status messages.
                println!("{code}");
                let _ = io::stdout().flush();
                eprintln!("Ready: {details}");
                eprintln!("Join endpoints are reserved; the transport may still be connecting or retrying.");
            }
            Event::Stats(s) if cli.diagnostics => {
                let method = format!("{}|{}|{}|{}|{}", s.phase, s.path, s.direct_endpoint, s.derp_region, s.service_status);
                if method != last_method || last_stats.elapsed() >= Duration::from_secs(5) {
                    eprintln!("Phase={} retry={} ({}s); service={}; traffic tx={} rx={} flows={}; method={}, latency={:.1}ms, endpoint={}, DERP={}",
                        s.phase, s.retry_attempt, s.retry_seconds, s.service_status,
                        s.tunnel_tx_bytes, s.tunnel_rx_bytes, s.active_flows,
                        if s.path.is_empty() {"unknown"} else {s.path.as_str()},
                        s.latency_ms, s.direct_endpoint, s.derp_region);
                    if !s.last_error.is_empty() { eprintln!("Latest transport error: {}", s.last_error); }
                    last_method = method;
                    last_stats = Instant::now();
                }
            }
            Event::Stats(s) => {
                if s.phase != last_phase {
                    eprintln!("Transport phase: {}{}", s.phase,
                        if s.phase == "reconnecting" {
                            format!(" (retry {} in {}s)",s.retry_attempt,s.retry_seconds)
                        } else {String::new()});
                    if s.phase == "reconnecting" && !s.last_error.is_empty() {
                        eprintln!("Reason: {}. Local ports remain reserved.",s.last_error);
                    }
                    last_phase = s.phase;
                }
            },
            Event::Error(err) => { eprintln!("Bridge error: {err}"); failure = Some(err); }
            Event::Stopped => break,
        }
    }
    stop.store(true, Ordering::SeqCst);
    worker.join().map_err(|_| "Bridge thread panicked".to_string())?;
    if let Some(err) = failure {Err(err)} else {Ok(())}
}

#[cfg(test)]
mod tests {
    use super::*;
    fn argv(xs: &[&str]) -> Vec<String> { xs.iter().map(|s| s.to_string()).collect() }
    #[test]
    fn host_generic_options() {
        let cmd = parse_options(&argv(&["host", "generic", "--port", "8080", "--protocol", "both"])) .unwrap();
        assert!(matches!(prepare(&cmd).unwrap(), Work::HostGeneric {ref ports, protocol:Protocol::Both, ..} if ports.as_slice() == &[8080][..]));
    }
    #[test]
    fn default_minecraft_protocols() {
        let java = parse_options(&argv(&["host", "minecraft"])) .unwrap();
        assert!(matches!(prepare(&java).unwrap(), Work::HostGeneric {ref ports, protocol:Protocol::Tcp, ..} if ports.as_slice() == &[25565][..]));
        let bedrock = parse_options(&argv(&["host", "minecraft", "--edition", "bedrock"])) .unwrap();
        assert!(matches!(prepare(&bedrock).unwrap(), Work::HostGeneric {ref ports, protocol:Protocol::Both, ..} if ports.as_slice() == &[19132][..]));
    }
    #[test]
    fn terraria_preset_and_port_override() {
        let default = parse_options(&argv(&["host", "terraria"])).unwrap();
        assert!(matches!(prepare(&default).unwrap(), Work::HostGeneric {
            ref name, ref preset, ref ports, protocol: Protocol::Tcp, ..
        } if name == "Terraria" && preset == "terraria" && ports.as_slice() == &[7777]));
        let custom = parse_options(&argv(&["host", "terraria", "--port", "7777-7779"])).unwrap();
        assert!(matches!(prepare(&custom).unwrap(), Work::HostGeneric {
            ref ports, protocol: Protocol::Tcp, ..
        } if ports.as_slice() == &[7777, 7778, 7779]));
        let invalid = parse_options(&argv(&["host", "terraria", "--protocol", "udp"])).unwrap();
        assert!(prepare(&invalid).is_err());
        let join = parse_options(&argv(&["join", "terraria", "--code", "TCBR1:example"])).unwrap();
        assert!(matches!(prepare(&join).unwrap(), Work::JoinCode {ref preset, ..} if preset == "terraria"));
    }
    #[test]
    fn autodetect_generic_share_code() {
        let code = crate::generic::encode(&crate::generic::GenericInvite::new(
            "tcTEST".into(), 25565, Protocol::Tcp, "Minecraft Java".into(), "minecraft".into()
        )).unwrap();
        let cmd = parse_options(&argv(&["join", "--code", &code])).unwrap();
        assert!(matches!(prepare(&cmd).unwrap(), Work::JoinCode { .. }));
    }
    #[test]
    fn autodetect_hytale_share_code() {
        let original = serde_json::json!({"Candidates":[{"Address":"192.168.1.10","Port":5520,"Type":"Host"}], "ExpiresAt":"2099-01-01T00:00:00Z"});
        let code = invite::encode_wrapper(&original, "tcTEST", 5520, Ipv4Addr::LOCALHOST).unwrap();
        let cmd = parse_options(&argv(&["join", "--code", &code])).unwrap();
        assert!(matches!(prepare(&cmd).unwrap(), Work::JoinCode { .. }));
    }
    #[test]
    fn join_local_port_override() {
        let cmd = parse_options(&argv(&["join", "--code", "games.example.com", "--listen-port", "26000"])).unwrap();
        assert!(matches!(prepare(&cmd).unwrap(), Work::JoinCode {local_port:Some(ref p), ..} if p.as_slice() == &[26000][..]));
    }
    #[test]
    fn invalid_options() {
        for args in [
            argv(&["host", "generic", "--port", "0"]),
            argv(&["host", "generic", "--port", "8080", "--unknown", "x"]),
            argv(&["join", "generic", "--code", "a", "--code-file", "b"]),
            argv(&["host", "hytale", "--port", "5520", "--code", "x"]),
            argv(&["join", "--code", "games.example.com", "--listen-port", "0"]),
        ] {
            assert!(parse_options(&args).and_then(|c| prepare(&c)).is_err());
        }
    }
}

#[cfg(test)]
mod multi_port_cli_tests {
    use super::*;
    #[test]
    fn accepts_multiport_cli() {
        let args = ["host","generic","--port","25566, 25567-25568","--protocol","both"];
        let cli = parse_options(&args.iter().map(|s| s.to_string()).collect::<Vec<_>>()).unwrap();
        assert!(matches!(prepare(&cli).unwrap(), Work::HostGeneric {ports, protocol:Protocol::Both, ..} if ports == vec![25566,25567,25568]));
    }
}
