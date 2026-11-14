use eframe::egui;

use crate::{SAPPHIRE, SUBTEXT};

pub fn show(ctx: &egui::Context, open: &mut bool) {
    egui::Window::new("About HyTail")
        .open(open)
        .resizable(false)
        .collapsible(false)
        .default_width(360.0)
        .show(ctx, |ui| {
            ui.heading(egui::RichText::new("HyTail").color(SAPPHIRE));
            ui.label("By Lucas \"fptbb\" Oliveira and contributors");
            ui.label("Copyright © 2026 Lucas \"fptbb\" Oliveira");
            ui.add_space(8.0);
            ui.label(egui::RichText::new("HyTail is licensed under MIT.").color(SUBTEXT));
            ui.hyperlink_to("Tailcat · BSD-3-Clause", "https://github.com/tailscale/tailcat");
            ui.hyperlink_to(
                "Third-party notices and dependency attribution",
                "https://github.com/fptbb/hytail/blob/main/THIRD_PARTY_LICENSES.md",
            );
            ui.label(
                egui::RichText::new("Third-party components retain their own licenses.")
                    .small()
                    .color(SUBTEXT),
            );
        });
}
