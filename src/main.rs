mod bridge;
mod invite;
mod generic;
mod ports;
mod cli;
mod join_detect;
mod about;

use bridge::{Event, Work, TunnelStats};
use generic::Protocol;
use join_detect::JoinPreset;
use eframe::egui;
use std::{
    sync::{atomic::{AtomicBool, Ordering}, mpsc::{self, Receiver}, Arc},
    thread::JoinHandle,
    time::Duration,
};

// Catppuccin Mocha / Sapphire: https://github.com/catppuccin/catppuccin
const BASE: egui::Color32 = egui::Color32::from_rgb(30, 30, 46);
const MANTLE: egui::Color32 = egui::Color32::from_rgb(24, 24, 37);
const SURFACE0: egui::Color32 = egui::Color32::from_rgb(49, 50, 68);
const SURFACE1: egui::Color32 = egui::Color32::from_rgb(69, 71, 90);
const TEXT: egui::Color32 = egui::Color32::from_rgb(205, 214, 244);
const SUBTEXT: egui::Color32 = egui::Color32::from_rgb(166, 173, 200);
const SAPPHIRE: egui::Color32 = egui::Color32::from_rgb(116, 199, 236);
const GREEN: egui::Color32 = egui::Color32::from_rgb(166, 227, 161);
const RED: egui::Color32 = egui::Color32::from_rgb(243, 139, 168);
const PINK: egui::Color32 = egui::Color32::from_rgb(245, 194, 231);

// A tiny vector heart avoids relying on missing emoji glyphs in egui's default fonts.
// Keeping this as geometry rather than an extra font works on Linux and Windows.
fn draw_heart(ui: &mut egui::Ui) {
    let (rect, _) = ui.allocate_exact_size(egui::vec2(17.0, 17.0), egui::Sense::hover());
    if !ui.is_rect_visible(rect) { return; }
    let mid = rect.center();
    let painter = ui.painter();
    painter.circle_filled(mid + egui::vec2(-3.2, -2.0), 3.8, PINK);
    painter.circle_filled(mid + egui::vec2(3.2, -2.0), 3.8, PINK);
    painter.add(egui::Shape::convex_polygon(
        vec![
            mid + egui::vec2(-6.7, -1.4),
            mid + egui::vec2(6.7, -1.4),
            mid + egui::vec2(0.0, 7.0),
        ],
        PINK,
        egui::Stroke::NONE,
    ));
}

fn configure_style(ctx: &egui::Context) {
    let mut style = (*ctx.style()).clone();
    let mut visuals = egui::Visuals::dark();
    visuals.override_text_color = None;
    visuals.weak_text_color = Some(SUBTEXT);
    visuals.text_edit_bg_color = Some(MANTLE);
    visuals.panel_fill = BASE;
    visuals.window_fill = BASE;
    visuals.extreme_bg_color = MANTLE;
    visuals.faint_bg_color = SURFACE0;
    visuals.hyperlink_color = SAPPHIRE;
    visuals.selection.bg_fill = SAPPHIRE;
    visuals.selection.stroke.color = MANTLE;
    visuals.widgets.noninteractive.bg_fill = BASE;
    visuals.widgets.noninteractive.fg_stroke.color = TEXT;
    visuals.widgets.inactive.bg_fill = SURFACE0;
    visuals.widgets.inactive.weak_bg_fill = SURFACE0;
    visuals.widgets.inactive.fg_stroke.color = TEXT;
    visuals.widgets.hovered.bg_fill = SURFACE1;
    visuals.widgets.hovered.weak_bg_fill = SURFACE1;
    visuals.widgets.hovered.fg_stroke.color = TEXT;
    visuals.widgets.active.bg_fill = SAPPHIRE;
    visuals.widgets.active.weak_bg_fill = SAPPHIRE;
    visuals.widgets.active.fg_stroke.color = MANTLE;
    style.visuals = visuals;
    style.spacing.item_spacing = egui::vec2(9.0, 9.0);
    style.spacing.button_padding = egui::vec2(15.0, 9.0);
    style.text_styles.insert(egui::TextStyle::Body, egui::FontId::proportional(16.0));
    style.text_styles.insert(egui::TextStyle::Button, egui::FontId::proportional(16.0));
    style.text_styles.insert(egui::TextStyle::Heading, egui::FontId::proportional(23.0));
    style.text_styles.insert(egui::TextStyle::Small, egui::FontId::proportional(14.0));
    style.text_styles.insert(egui::TextStyle::Monospace, egui::FontId::monospace(15.0));
    ctx.set_style(style);
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Side { Host, Join }
#[derive(Clone, Copy, PartialEq, Eq)]
enum Preset { Generic, Hytale, Minecraft, Terraria }
#[derive(Clone, Copy, PartialEq, Eq)]
enum MinecraftEdition { Java, Bedrock }
impl MinecraftEdition {
    fn name(self) -> &'static str { match self { Self::Java => "Java (TCP)", Self::Bedrock => "Bedrock (TCP + UDP)" } }
    fn port(self) -> u16 { match self { Self::Java => 25565, Self::Bedrock => 19132 } }
    fn protocol(self) -> Protocol { match self { Self::Java => Protocol::Tcp, Self::Bedrock => Protocol::Both } }
}
struct Worker { stop: Arc<AtomicBool>, handle: JoinHandle<()> }
struct App {
    side: Side,
    preset: Preset,
    edition: MinecraftEdition,
    protocol: Protocol,
    port: String,
    target: String,
    label: String,
    input: String,
    local_port: String,
    output: String,
    details: String,
    status: String,
    is_error: bool,
    active: bool,
    show_diagnostics: bool,
    show_about: bool,
    stats: TunnelStats,
    rx: Option<Receiver<Event>>,
    worker: Option<Worker>,
}
impl Default for App {
    fn default() -> Self {
        Self {
            side: Side::Host, preset: Preset::Generic, edition: MinecraftEdition::Java,
            protocol: Protocol::Tcp, port: "25565".into(), target: "127.0.0.1".into(),
            label: "My service".into(), input: String::new(), local_port: String::new(), output: String::new(),
            details: String::new(), status: "Ready".into(), is_error: false, active: false,
            show_diagnostics: false, show_about: false, stats: TunnelStats::default(), rx: None, worker: None,
        }
    }
}
impl App {
    fn choose_preset(&mut self, preset: Preset) {
        if self.preset == preset { return; }
        self.preset = preset;
        if self.side == Side::Host { self.input.clear(); }
        self.output.clear(); self.details.clear(); self.status = "Ready".into();
        self.is_error = false;
        if preset == Preset::Minecraft { self.port = "25565".into(); self.protocol = Protocol::Tcp; self.label = "Minecraft Java".into(); }
        if preset == Preset::Terraria { self.port = "7777".into(); self.protocol = Protocol::Tcp; self.label = "Terraria".into(); }
    }
    fn select_join_preset(&mut self, detected: JoinPreset) {
        // Preserve the pasted invitation; the invitation determines the joining service and ports.
        self.preset = match detected {
            JoinPreset::Hytale => Preset::Hytale,
            JoinPreset::MinecraftJava => {
                self.edition = MinecraftEdition::Java;
                Preset::Minecraft
            },
            JoinPreset::MinecraftBedrock => {
                self.edition = MinecraftEdition::Bedrock;
                Preset::Minecraft
            },
            JoinPreset::Terraria => Preset::Terraria,
            JoinPreset::Generic => Preset::Generic,
        };
    }

    fn recognize_join_input(&mut self) {
        if let Ok(Some(preset)) = join_detect::classify(&self.input) {
            self.side = Side::Join;
            self.select_join_preset(preset);
        }
    }

    fn preferred_port(&self) -> Result<Option<Vec<u16>>, String> {
        if self.local_port.trim().is_empty() { return Ok(None); }
        Ok(Some(ports::parse(&self.local_port)?))
    }
    fn start(&mut self) {
        let request: Result<Work, String> = (|| match (self.side, self.preset) {
            (Side::Host, Preset::Hytale) => {
                let game = invite::decode_hytale(&self.input)?;
                invite::validate_expiry(&game)?;
                let port = invite::choose_port(&game)?;
                Ok(Work::HostHytale { original: game, port })
            }
            (Side::Join, Preset::Hytale) => Ok(Work::JoinCode {
                input: self.input.trim().into(), preset: "auto".into(), local_port: self.preferred_port()?,
            }),
            (Side::Host, Preset::Generic | Preset::Minecraft | Preset::Terraria) => {
                let ports = ports::parse(&self.port)?;
                let ip = self.target.trim().parse().map_err(|_| "Target must be an IPv4 address")?;
                let (protocol, name, preset) = match self.preset {
                    Preset::Minecraft => (
                        self.edition.protocol(),
                        format!("Minecraft {}", match self.edition { MinecraftEdition::Java => "Java", MinecraftEdition::Bedrock => "Bedrock" }),
                        "minecraft",
                    ),
                    Preset::Terraria => (Protocol::Tcp, "Terraria".to_string(), "terraria"),
                    Preset::Generic => (self.protocol, self.label.trim().to_string(), "generic"),
                    Preset::Hytale => unreachable!(),
                };
                if name.is_empty() || name.len() > 120 { return Err("Enter a service name (1-120 characters)".into()); }
                Ok(Work::HostGeneric { name, preset: preset.into(), protocol, ports, target: ip })
            }
            (Side::Join, Preset::Generic | Preset::Minecraft | Preset::Terraria) => Ok(Work::JoinCode {
                input: self.input.trim().into(),
                preset: "auto".into(),
                local_port: self.preferred_port()?,
            }),
        })();
        let work = match request { Ok(w) => w, Err(e) => { self.status=e; self.is_error=true; return; } };
        let (tx, rx) = mpsc::channel();
        let stop = Arc::new(AtomicBool::new(false));
        let handle = bridge::spawn(work, Arc::clone(&stop), tx);
        self.rx = Some(rx); self.worker = Some(Worker {stop,handle});
        self.output.clear(); self.details.clear(); self.stats = TunnelStats::default();
        self.status = "Connecting...".into(); self.active = true; self.is_error = false;
    }
    fn stop(&mut self) {
        if let Some(w) = &self.worker { w.stop.store(true, Ordering::Relaxed); }
        self.status = "Stopping...".into();
    }
    fn receive(&mut self) {
        let updates: Vec<Event> = self.rx.as_ref().map(|rx| rx.try_iter().collect()).unwrap_or_default();
        for event in updates {
            match event {
                Event::DetectedPreset(preset) => self.select_join_preset(preset),
                Event::Ready {code,details} => {
                    self.output=code;
                    self.details=details;
                    self.status=if self.side==Side::Join {"Local endpoint reserved; connecting...".into()}
                        else {"Hosting bridge".into()};
                }
                Event::Error(e) => { self.status=e; self.is_error=true; }
                Event::Stats(s) => {
                    if !self.is_error {
                        self.status = match s.phase.as_str() {
                            "connecting" => "Local ports reserved; connecting to peer...".into(),
                            "reconnecting" => format!("Reconnecting (attempt {}, next retry {}s)", s.retry_attempt, s.retry_seconds),
                            "online" => "Bridge online".into(),
                            "hosting" => "Hosting bridge".into(),
                            "stopped" => "Stopped".into(),
                            _ => self.status.clone(),
                        };
                    }
                    self.stats=s;
                }
                Event::Stopped => {
                    if let Some(w) = self.worker.take() { let _ = w.handle.join(); }
                    self.rx=None;self.active=false;
                    if !self.is_error { self.status="Stopped".into(); }
                }
            }
        }
    }
    fn show_diagnostics(&mut self, ctx: &egui::Context) {
        let stats = self.stats.clone();
        let status = self.status.clone();
        egui::Window::new("Tailcat diagnostics")
            .open(&mut self.show_diagnostics).resizable(true).default_width(475.0)
            .show(ctx, |ui| {
                ui.label(egui::RichText::new("Network transport").strong().color(SAPPHIRE));
                ui.add_space(6.0);
                let line = |ui: &mut egui::Ui, key: &str, value: String| {
                    ui.horizontal_wrapped(|ui| {ui.label(egui::RichText::new(format!("{key}:")).color(SUBTEXT));ui.label(value);});
                };
                line(ui, "State", status);
                line(ui, "Transport phase", stats.phase.clone());
                if stats.retry_attempt>0 {
                    line(ui, "Reconnect attempt", format!("{} (retry after {}s)",stats.retry_attempt,stats.retry_seconds));
                }
                if stats.last_connected_unix>0 { line(ui,"Last connected (Unix)",stats.last_connected_unix.to_string()); }
                if !stats.last_error.is_empty() { line(ui,"Latest transport error",stats.last_error.clone()); }
                line(ui, "Role", stats.mode.clone());
                line(ui, "Protocol", stats.protocol.to_uppercase());
                line(ui, "Host-side target / join bind", stats.target_ip.clone());
                line(ui, "Remote service ports", if stats.remote_ports.is_empty() { stats.remote_port.to_string() } else { ports::csv(&stats.remote_ports) });
                if !stats.local_ports.is_empty() {
                    line(ui, "Local listening ports", ports::csv(&stats.local_ports));
                    line(ui, "Port selection", stats.local_port_policy.clone());
                }
                line(ui, "Service availability", stats.service_status.clone());
                for probe in &stats.service_ports {
                    line(ui, &format!("{} {}",probe.protocol.to_uppercase(),probe.port), probe.status.clone());
                }
                line(ui, "Active TCP / UDP flows", stats.active_flows.to_string());
                line(ui, "Session uptime", format!("{} s", stats.uptime_seconds));
                ui.separator();
                ui.label(egui::RichText::new("Path discovery (joining peer)").strong().color(SAPPHIRE));
                let path = match stats.path.as_str() {
                    "direct" => "Direct UDP peer-to-peer",
                    "derp" => "DERP relay",
                    "unknown" | "" => "Not measured",
                    other => other,
                };
                line(ui, "Connection method", path.into());
                if !stats.direct_endpoint.is_empty() { line(ui,"Direct endpoint",stats.direct_endpoint.clone()); }
                if !stats.derp_region.is_empty() { line(ui,"DERP region",stats.derp_region.clone()); }
                if stats.latency_ms > 0.0 {line(ui,"Discovery round-trip",format!("{:.1} ms",stats.latency_ms));}
                if !stats.path_error.is_empty() { line(ui,"Probe error",stats.path_error.clone()); }
                if stats.mode == "host" {
                    ui.label(egui::RichText::new("The host can check local TCP listeners, but cannot prove UDP server availability or determine the remote client path.").color(SUBTEXT));
                }
                ui.separator();
                ui.label(egui::RichText::new("Forwarded traffic").strong().color(SAPPHIRE));
                line(ui,"Sent into tunnel",format!("{} bytes / {} operations",stats.tunnel_tx_bytes,stats.tunnel_tx_packets));
                line(ui,"Received from tunnel",format!("{} bytes / {} operations",stats.tunnel_rx_bytes,stats.tunnel_rx_packets));
                if stats.last_activity_unix != 0 { line(ui,"Last activity (Unix)",stats.last_activity_unix.to_string()); }
                ui.add_space(5.0);
                ui.label(egui::RichText::new("UDP availability remains unverified until application-specific traffic receives a response. A direct path on the same PC does not prove NAT traversal. TCP counts are stream operations; UDP counts are datagrams. The displayed path is from the latest discovery probe.").small().color(SUBTEXT));
            });
    }
}
impl Drop for App {
    fn drop(&mut self) {
        if let Some(w) = self.worker.take() {w.stop.store(true,Ordering::Relaxed);let _ = w.handle.join();}
    }
}

impl eframe::App for App {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.receive();
        egui::TopBottomPanel::bottom("footer").show(ctx, |ui| {
            ui.horizontal(|ui| {
                ui.label(egui::RichText::new("MIT licensed | Built upon").color(SUBTEXT).small());
                ui.hyperlink_to(egui::RichText::new("Tailcat").color(SAPPHIRE).small(), "https://github.com/tailscale/tailcat");
                ui.label(egui::RichText::new("by").color(SUBTEXT).small());
                ui.hyperlink_to(egui::RichText::new("Tailscale").color(SAPPHIRE).small(), "https://tailscale.com");
                draw_heart(ui);
                ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                    if ui.small_button(egui::RichText::new("©").color(SAPPHIRE)).on_hover_text("About and third-party licenses").clicked() {
                        self.show_about = true;
                    }
                });
            });
        });
        egui::CentralPanel::default().show(ctx, |ui| {
            egui::ScrollArea::vertical().auto_shrink([false,false]).show(ui, |ui| {
                ui.add_space(6.0);
                ui.horizontal(|ui| {
                    ui.heading(egui::RichText::new("HyTail").color(SAPPHIRE));
                    ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                        if ui.small_button("Quit").clicked() { ctx.send_viewport_cmd(egui::ViewportCommand::Close); }
                        if ui.small_button("Diagnostics").clicked() { self.show_diagnostics=true; }
                    });
                });
                ui.label(egui::RichText::new("Private connections through Tailcat - no router configuration").color(SUBTEXT));
                ui.add_space(9.0);
                ui.add_enabled_ui(!self.active, |ui| {
                    ui.horizontal_wrapped(|ui| {
                        if ui.selectable_label(self.preset == Preset::Generic,"Generic bridge").clicked() {self.choose_preset(Preset::Generic);}
                        if ui.selectable_label(self.preset == Preset::Hytale,"Hytale").clicked() {self.choose_preset(Preset::Hytale);}
                        if ui.selectable_label(self.preset == Preset::Minecraft,"Minecraft").clicked() {self.choose_preset(Preset::Minecraft);}
                        if ui.selectable_label(self.preset == Preset::Terraria,"Terraria").clicked() {self.choose_preset(Preset::Terraria);}
                    });
                    ui.horizontal(|ui| {
                        ui.selectable_value(&mut self.side, Side::Host, "Host");
                        ui.selectable_value(&mut self.side, Side::Join, "Join");
                    });
                });
                ui.add_space(6.0);
                if self.preset == Preset::Minecraft && self.side == Side::Host {
                    let previous_edition = self.edition;
                    ui.add_enabled_ui(!self.active, |ui| {
                        egui::ComboBox::from_label("Minecraft edition")
                            .selected_text(self.edition.name())
                            .show_ui(ui, |ui| {
                                ui.selectable_value(&mut self.edition,MinecraftEdition::Java,"Java (TCP / 25565)");
                                ui.selectable_value(&mut self.edition,MinecraftEdition::Bedrock,"Bedrock (TCP + UDP / 19132)");
                            });
                    });
                    if previous_edition != self.edition { self.port = self.edition.port().to_string(); }
                    // Minecraft ports are presets, not locked values.
                    if self.edition == MinecraftEdition::Bedrock {
                        ui.label(egui::RichText::new(
                            "Bedrock NetherNet may negotiate extra UDP ports; this preset only bridges the selected TCP+UDP port."
                        ).color(SUBTEXT).small());
                    }
                }
                if self.side == Side::Host && self.preset != Preset::Hytale {
                    if self.preset == Preset::Generic {
                        ui.label("Service name");
                        ui.add_enabled(!self.active,egui::TextEdit::singleline(&mut self.label).desired_width(f32::INFINITY));
                        ui.horizontal(|ui| {
                            ui.label("Protocol");
                            ui.add_enabled_ui(!self.active, |ui| {
                                egui::ComboBox::from_id_salt("bridge-protocol")
                                    .selected_text(self.protocol.as_str().to_uppercase())
                                    .show_ui(ui, |ui| {
                                        ui.selectable_value(&mut self.protocol,Protocol::Tcp,"TCP");
                                        ui.selectable_value(&mut self.protocol,Protocol::Udp,"UDP");
                                        ui.selectable_value(&mut self.protocol,Protocol::Both,"TCP + UDP");
                                    });
                            });
                        });
                    }
                    ui.horizontal(|ui| {
                        ui.label("Service ports");
                        ui.add_enabled(!self.active,egui::TextEdit::singleline(&mut self.port).desired_width(220.0));
                        if !self.active {
                            let default_port = match self.preset {
                                Preset::Minecraft => Some(self.edition.port()),
                                Preset::Terraria => Some(7777),
                                _ => None,
                            };
                            if let Some(default_port) = default_port {
                                if ui.small_button("Use default").clicked() { self.port = default_port.to_string(); }
                            }
                        }
                    });
                    ui.label(egui::RichText::new("Examples: 25566, 25567 or 25566-25568 (max 32 ports)").color(SUBTEXT).small());
                    ui.label("Destination IP on this computer");
                    ui.add_enabled(!self.active,egui::TextEdit::singleline(&mut self.target).desired_width(f32::INFINITY));
                    ui.label(egui::RichText::new("Use 127.0.0.1 or an assigned private IPv4. Only this exact service is exposed.").color(SUBTEXT).small());
                } else {
                    let prompt = if self.side == Side::Join {
                        "Paste a bridge invitation (type detected automatically)"
                    } else { "Paste the original Hytale multiplayer code" };
                    ui.label(prompt);
                    let mut edited = false;
                    ui.add_enabled_ui(!self.active, |ui| {
                        edited = ui.add_sized([ui.available_width(),95.0], egui::TextEdit::multiline(&mut self.input)
                            .font(egui::TextStyle::Monospace).hint_text("Paste your bridge invitation")).changed();
                    });
                    // A bridge invitation pasted into a Host field selects Join automatically.
                    if edited { self.recognize_join_input(); }
                    if self.side == Side::Join {
                        ui.horizontal(|ui| {
                            ui.label("Local ports (optional)");
                            ui.add_enabled(!self.active, egui::TextEdit::singleline(&mut self.local_port)
                                .desired_width(220.0).hint_text("Automatic / start port / list"));
                        });
                    }
                }
                ui.add_space(10.0);
                ui.horizontal_wrapped(|ui| {
                    if self.active {
                        if ui.button("Stop bridge").clicked() {self.stop();}
                    } else if ui.button(if self.side == Side::Host {"Start hosting"} else {"Connect"}).clicked() {self.start();}
                    let color = if self.is_error { RED }
                        else if self.stats.phase == "reconnecting" { PINK }
                        else if self.stats.phase == "online" || self.stats.phase == "hosting" { GREEN }
                        else { SUBTEXT };
                    ui.label(egui::RichText::new(&self.status).color(color));
                });
                if !self.details.is_empty() { ui.label(egui::RichText::new(&self.details).color(SUBTEXT)); }
                if self.stats.phase == "reconnecting" && !self.stats.last_error.is_empty() {
                    ui.label(egui::RichText::new(format!("Transport unavailable: {}. Local ports remain reserved.",self.stats.last_error)).color(PINK).small());
                }
                if self.side == Side::Host && self.active && self.stats.service_status == "tcp-unreachable" {
                    ui.label(egui::RichText::new("Warning: the configured local TCP service is not listening. Start the game server or check its port.").color(PINK));
                }
                if self.active && !self.output.is_empty() {
                    ui.label(egui::RichText::new(format!("Tunnel traffic: sent {} bytes / received {} bytes", self.stats.tunnel_tx_bytes,self.stats.tunnel_rx_bytes)).color(TEXT));
                    if self.stats.tunnel_tx_bytes > 0 && self.stats.tunnel_rx_bytes > 0 {
                        ui.label(egui::RichText::new("Bidirectional traffic confirmed").color(GREEN));
                    }
                }
                if !self.output.is_empty() {
                    ui.separator();
                    ui.horizontal(|ui| {
                        ui.strong(match (self.side,self.preset) {
                            (Side::Host,_) => "Share this private bridge code",
                            (Side::Join,Preset::Hytale) => "Paste this into Hytale",
                            (Side::Join,_) if self.details.contains("Hytale ->") => "Paste this into Hytale",
                            (Side::Join,_) => "Connect to this address and port",
                        });
                        if ui.button("Copy").clicked() { ctx.copy_text(self.output.clone()); }
                    });
                    ui.add_sized([ui.available_width(),if self.side == Side::Host || self.preset == Preset::Hytale {90.0} else {42.0}],
                        egui::TextEdit::multiline(&mut self.output).interactive(false).font(egui::TextStyle::Monospace));
                    ui.label(egui::RichText::new("Keep HyTail open while using the connection. Treat share codes as secrets.").color(SUBTEXT).small());
                }
            });
        });
        if self.show_diagnostics { self.show_diagnostics(ctx); }
        if self.show_about { about::show(ctx, &mut self.show_about); }
        if self.active {ctx.request_repaint_after(Duration::from_millis(250));}
    }
}
// The same executable is a GUI by default and a command-line application
// whenever a CLI argument is supplied. Keep the Windows console subsystem so
// PowerShell/CMD can capture help, bridge codes, diagnostics and errors.

// NixOS and other non-FHS systems may have a Wayland session but not expose
// libwayland-client.so.0 through the runtime loader. winit otherwise aborts
// event-loop creation with an error that is unrelated to the bridge/CLI.
#[cfg(target_os = "linux")]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum LinuxGuiBackend { Wayland, X11 }

#[cfg(target_os = "linux")]
fn choose_linux_gui_backend(
    requested: &str,
    wayland_session: bool,
    x11_session: bool,
    wayland_library: bool,
    x11_library: bool,
) -> Result<LinuxGuiBackend, String> {
    let guidance = "Install/expose libwayland-client.so.0 (Wayland) or libX11.so.6 (X11/XWayland) to the dynamic loader. On NixOS, package HyTail with its graphics libraries or configure nix-ld; ordinary Linux binaries are not automatically portable there. You can select HYTAIL_GUI_BACKEND=x11 or wayland.";
    match requested {
        "auto" => {
            if wayland_session && wayland_library {
                Ok(LinuxGuiBackend::Wayland)
            } else if x11_session && x11_library {
                Ok(LinuxGuiBackend::X11)
            } else {
                Err(format!("No usable Linux window backend: Wayland session={wayland_session}, Wayland library={wayland_library}, X11 display={x11_session}, X11 library={x11_library}. {guidance}"))
            }
        }
        "wayland" if wayland_session && wayland_library => Ok(LinuxGuiBackend::Wayland),
        "x11" if x11_session && x11_library => Ok(LinuxGuiBackend::X11),
        "x11" | "wayland" => Err(format!("Requested {requested} backend is unavailable. {guidance}")),
        _ => Err("HYTAIL_GUI_BACKEND must be 'auto', 'wayland' or 'x11'.".into()),
    }
}

#[cfg(target_os = "linux")]
fn select_linux_gui_backend() -> Result<LinuxGuiBackend, String> {
    // dlopen (rather than probing known distro paths) follows the same library
    // search path that winit uses, including LD_LIBRARY_PATH on NixOS.
    let loadable = |name| unsafe { libloading::Library::new(name) }.is_ok();
    let has_env = |name| std::env::var_os(name).is_some_and(|val| !val.is_empty());
    let wayland_session = has_env("WAYLAND_DISPLAY") || has_env("WAYLAND_SOCKET");
    let requested = std::env::var("HYTAIL_GUI_BACKEND")
        .or_else(|_| std::env::var("HYTAILSCALE_GUI_BACKEND"))
        .unwrap_or_else(|_| "auto".into()).to_ascii_lowercase();
    choose_linux_gui_backend(
        &requested,
        wayland_session,
        has_env("DISPLAY"),
        loadable("libwayland-client.so.0"),
        loadable("libX11.so.6"),
    )
}

fn run_gui() -> Result<(), String> {
    #[allow(unused_mut)]
    let mut options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_title("HyTail")
            .with_decorations(true)
            .with_titlebar_buttons_shown(true)
            .with_close_button(true)
            .with_minimize_button(true)
            .with_maximize_button(true)
            .with_resizable(true)
            .with_inner_size([690.0,540.0])
            .with_min_inner_size([520.0,410.0]),
        ..Default::default()
    };
    #[cfg(all(target_os = "windows", target_arch = "aarch64"))]
    {options.renderer = eframe::Renderer::Wgpu;}
    #[cfg(target_os = "linux")]
    {
        let backend = select_linux_gui_backend()?;
        options.event_loop_builder = Some(Box::new(move |builder| {
            match backend {
                LinuxGuiBackend::Wayland => {
                    use winit::platform::wayland::EventLoopBuilderExtWayland;
                    builder.with_wayland();
                }
                LinuxGuiBackend::X11 => {
                    use winit::platform::x11::EventLoopBuilderExtX11;
                    builder.with_x11();
                }
            }
        }));
    }
    eframe::run_native("HyTail",options,Box::new(|cc| {
        configure_style(&cc.egui_ctx);
        Ok(Box::new(App::default()))
    })).map_err(|error| {
        #[cfg(target_os = "linux")]
        { format!("{error}. Check Wayland/X11 libraries and graphics drivers. On NixOS, use a properly packaged application or nix-ld. With an XWayland session, try HYTAIL_GUI_BACKEND=x11.") }
        #[cfg(not(target_os = "linux"))]
        { error.to_string() }
    })
}

fn gui_requested(args: &[String]) -> bool {
    args.is_empty() || (args.len() == 1 && args[0] == "gui")
}

// A Windows console-subsystem executable started from Explorer gets its own
// console window. Detach only when *we* are the sole console process. A shell
// invocation keeps its console and has normal stdout/stderr for CLI commands.
// There can be a brief console flash when launching the GUI from Explorer.
#[cfg(windows)]
fn detach_standalone_gui_console() {
    #[link(name = "kernel32")]
    unsafe extern "system" {
        fn GetConsoleProcessList(ids: *mut u32, count: u32) -> u32;
        fn FreeConsole() -> i32;
    }
    let mut processes = [0u32; 2];
    let count = unsafe { GetConsoleProcessList(processes.as_mut_ptr(), processes.len() as u32) };
    if count == 1 {
        unsafe { FreeConsole() };
    }
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if gui_requested(&args) {
        #[cfg(windows)]
        detach_standalone_gui_console();
        if let Err(err) = run_gui() {
            eprintln!("HyTail GUI error: {err}");
            std::process::exit(1);
        }
    } else if let Err(err) = cli::run_from(args) {
        eprintln!("error: {err}");
        eprintln!("Run 'hytail --help' for usage.");
        std::process::exit(2);
    }
}

#[cfg(test)]
mod entry_tests {
    use super::*;
    fn args(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| s.to_string()).collect()
    }
    #[test]
    fn gui_only_with_no_args_or_gui_subcommand() {
        assert!(gui_requested(&args(&[])));
        assert!(gui_requested(&args(&["gui"])));
        for argv in [
            args(&["--help"]), args(&["-h"]), args(&["help"]),
            args(&["host", "minecraft"]), args(&["join", "--code", "x"]),
            args(&["gui", "--help"]),
        ] { assert!(!gui_requested(&argv)); }
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_window_backend_selection() {
        use LinuxGuiBackend::{Wayland, X11};
        let choose = choose_linux_gui_backend;
        assert_eq!(choose("auto", true, true, true, true).unwrap(), Wayland);
        assert_eq!(choose("auto", true, true, false, true).unwrap(), X11);
        assert_eq!(choose("auto", false, true, true, true).unwrap(), X11);
        assert_eq!(choose("wayland", true, true, true, true).unwrap(), Wayland);
        assert_eq!(choose("x11", true, true, true, true).unwrap(), X11);
        assert!(choose("auto", true, true, false, false).is_err());
        assert!(choose("wayland", true, true, false, true).is_err());
        assert!(choose("x11", true, false, true, true).is_err());
        assert!(choose("invalid", true, true, true, true).is_err());
    }
}
