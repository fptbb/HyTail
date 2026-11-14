//! Protocol-neutral Tailcat share codes. The code is a capability: keep it private.
use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
use chrono::{DateTime, Duration, Utc};
use flate2::{read::DeflateDecoder, write::DeflateEncoder, Compression};
use serde::{Deserialize, Serialize};
use std::io::{Read, Write};

const PREFIX: &str = "TCBR1:";
const MAX_JSON: u64 = 32_768;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Protocol { Tcp, Udp, Both }
impl Protocol {
    pub fn as_str(self) -> &'static str {
        match self { Self::Tcp => "tcp", Self::Udp => "udp", Self::Both => "both" }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct GenericInvite {
    #[serde(rename = "Version")]
    pub version: u8,
    #[serde(rename = "TailcatAddress")]
    pub address: String,
    #[serde(rename = "RemotePort")]
    pub port: u16,
    #[serde(rename = "RemotePorts", default, skip_serializing_if = "Vec::is_empty")]
    pub ports: Vec<u16>,
    #[serde(rename = "Protocol")]
    pub protocol: Protocol,
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Preset")]
    pub preset: String,
    #[serde(rename = "ExpiresAt")]
    pub expires_at: String,
}

impl GenericInvite {
    pub fn new(address: String, port: u16, protocol: Protocol, name: String, preset: String) -> Self {
        Self::new_multi(address, vec![port], protocol, name, preset)
    }

    pub fn new_multi(address: String, ports: Vec<u16>, protocol: Protocol, name: String, preset: String) -> Self {
        let port = ports.first().copied().unwrap_or(0);
        let expanded = if ports.len() > 1 { ports } else { Vec::new() };
        Self { version: 1, address, port, ports: expanded, protocol, name, preset,
            expires_at: (Utc::now() + Duration::hours(12)).to_rfc3339() }
    }

    pub fn remote_ports(&self) -> Vec<u16> {
        if self.ports.is_empty() { vec![self.port] } else { self.ports.clone() }
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.version != 1 { return Err("Unsupported generic invite version".into()); }
        if self.port == 0 { return Err("Invalid destination port".into()); }
        if !self.ports.is_empty() {
            if self.ports.len() < 2 || self.ports.len() > crate::ports::MAX_PORTS
                || self.ports[0] != self.port
                || self.ports.iter().any(|p| *p == 0)
                || self.ports.windows(2).any(|w| w[0] >= w[1]) {
                return Err("Invalid, duplicated, or unsorted RemotePorts".into());
            }
        }
        if !self.address.starts_with("tc") || self.address.len() > 8192
            || self.address.chars().any(char::is_whitespace) {
            return Err("Malformed Tailcat address".into());
        }
        if self.name.len() > 120 || self.preset.len() > 40 {
            return Err("Bridge metadata is too long".into());
        }
        let end = DateTime::parse_from_rfc3339(&self.expires_at)
            .map_err(|_| "Invalid expiration timestamp")?;
        if end <= Utc::now() { return Err("Bridge code expired; request a new one".into()); }
        Ok(())
    }
}

pub fn encode(value: &GenericInvite) -> Result<String, String> {
    value.validate()?;
    let json = serde_json::to_vec(value).map_err(|e| e.to_string())?;
    if json.len() as u64 > MAX_JSON { return Err("Invite too large".into()); }
    let mut z = DeflateEncoder::new(Vec::new(), Compression::best());
    z.write_all(&json).map_err(|e| e.to_string())?;
    Ok(format!("{PREFIX}{}", URL_SAFE_NO_PAD.encode(z.finish().map_err(|e| e.to_string())?)))
}

pub fn decode(input: &str) -> Result<GenericInvite, String> {
    if input.len() > 50_000 { return Err("Invite too long".into()); }
    let encoded = input.trim().strip_prefix(PREFIX).ok_or("Expected TCBR1: code")?;
    let compressed = URL_SAFE_NO_PAD.decode(encoded.as_bytes()).map_err(|_| "Invalid bridge Base64")?;
    let mut raw = Vec::new();
    DeflateDecoder::new(compressed.as_slice()).take(MAX_JSON + 1)
        .read_to_end(&mut raw).map_err(|_| "Invalid compressed bridge")?;
    if raw.len() as u64 > MAX_JSON { return Err("Bridge payload exceeds limit".into()); }
    let value: GenericInvite = serde_json::from_slice(&raw).map_err(|e| e.to_string())?;
    value.validate()?;
    Ok(value)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn generic_round_trip() {
        for p in [Protocol::Tcp, Protocol::Udp, Protocol::Both] {
            let x = GenericInvite::new("tcExample".into(), 25565, p, "MC Java".into(), "minecraft".into());
            let y = decode(&encode(&x).unwrap()).unwrap();
            assert_eq!(y.protocol, p);
            assert_eq!(y.port, 25565);
            assert_eq!(y.name, "MC Java");
        }
    }
    #[test]
    fn rejects_bad_capability_and_expiry() {
        let mut x = GenericInvite::new("tcExample".into(), 80, Protocol::Tcp, "test".into(), "generic".into());
        x.expires_at = "2020-01-01T00:00:00Z".into();
        assert!(encode(&x).is_err());
        assert!(decode("TCBR1:nonsense").is_err());
    }
}

#[cfg(test)]
mod multi_tests {
    use super::*;
    #[test]
    fn roundtrip_multi_and_old_single() {
        let a = GenericInvite::new_multi("tcExample".into(), vec![25566, 25567, 25568], Protocol::Both, "Game".into(), "generic".into());
        let b = decode(&encode(&a).unwrap()).unwrap();
        assert_eq!(b.remote_ports(), vec![25566, 25567, 25568]);
        let old = GenericInvite::new("tcExample".into(), 25565, Protocol::Tcp, "Game".into(), "generic".into());
        let serialized = serde_json::to_string(&old).unwrap();
        assert!(!serialized.contains("RemotePorts"));
        assert_eq!(decode(&encode(&old).unwrap()).unwrap().remote_ports(), vec![25565]);
    }
    #[test]
    fn rejects_out_of_order_ports() {
        let mut a = GenericInvite::new_multi("tcExample".into(),vec![4,3],Protocol::Udp,"Game".into(),"generic".into());
        assert!(a.validate().is_err());
        a.ports = vec![4,4];
        assert!(a.validate().is_err());
    }
}
