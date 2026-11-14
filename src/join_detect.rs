//! Local-only invitation classification for the GUI and CLI.
//! A raw domain is deliberately not queried while the user is typing.
//! The networking worker resolves DNS and reports the resulting preset.
use crate::{generic::{self, Protocol}, invite};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum JoinPreset { Generic, Hytale, MinecraftJava, MinecraftBedrock, Terraria }

impl JoinPreset {
    pub fn from_metadata(value: &str, protocol: Protocol) -> Self {
        match value.trim().to_ascii_lowercase().as_str() {
            "hytale" => Self::Hytale,
            "minecraft" if protocol == Protocol::Tcp => Self::MinecraftJava,
            "minecraft" => Self::MinecraftBedrock,
            "terraria" => Self::Terraria,
            _ => Self::Generic,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Generic => "generic",
            Self::Hytale => "hytale",
            Self::MinecraftJava => "minecraft-java",
            Self::MinecraftBedrock => "minecraft-bedrock",
            Self::Terraria => "terraria",
        }
    }
}

/// `None` means this is not an inline bridge code (possibly an advanced DNS
/// invitation); errors for malformed/expired prefixed codes are preserved.
pub fn classify(input: &str) -> Result<Option<JoinPreset>, String> {
    let code = input.trim();
    if code.starts_with("HTBR1:") {
        invite::decode_wrapper(code)?;
        Ok(Some(JoinPreset::Hytale))
    } else if code.starts_with("TCBR1:") {
        let parsed = generic::decode(code)?;
        Ok(Some(JoinPreset::from_metadata(&parsed.preset, parsed.protocol)))
    } else {
        Ok(None)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::generic::{GenericInvite, Protocol};
    use std::net::Ipv4Addr;

    fn generic_code(preset: &str) -> String {
        generic::encode(&GenericInvite::new(
            "tcExample".into(), 7777, Protocol::Tcp, "Server".into(), preset.into(),
        )).unwrap()
    }

    #[test]
    fn matches_known_presets_case_insensitively() {
        for (label, kind) in [
            ("minecraft", JoinPreset::MinecraftJava),
            ("TERRARIA", JoinPreset::Terraria),
            ("generic", JoinPreset::Generic),
            ("future-game", JoinPreset::Generic),
        ] {
            assert_eq!(classify(&generic_code(label)).unwrap(), Some(kind));
        }
    }

    #[test]
    fn recognizes_minecraft_bedrock_from_protocol() {
        let code = generic::encode(&GenericInvite::new(
            "tcExample".into(), 19132, Protocol::Both, "Bedrock".into(), "minecraft".into(),
        )).unwrap();
        assert_eq!(classify(&code).unwrap(), Some(JoinPreset::MinecraftBedrock));
    }

    #[test]
    fn recognizes_hytale_wrapper() {
        let raw = serde_json::json!({
            "Candidates": [{"Address":"192.168.1.4", "Port": 5520}],
            "ExpiresAt": "2099-01-01T00:00:00Z"
        });
        let code = invite::encode_wrapper(&raw, "tcExample", 5520, Ipv4Addr::LOCALHOST).unwrap();
        assert_eq!(classify(&code).unwrap(), Some(JoinPreset::Hytale));
    }

    #[test]
    fn malformed_bridge_not_treated_as_generic() {
        assert!(classify("TCBR1:garbage").is_err());
        assert!(classify("HTBR1:garbage").is_err());
        assert_eq!(classify("games.example.com").unwrap(), None);
        assert_eq!(classify("some unrelated text").unwrap(), None);
    }
}
