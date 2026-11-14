use base64::{Engine as _, engine::general_purpose::{STANDARD, STANDARD_NO_PAD, URL_SAFE, URL_SAFE_NO_PAD}};
use chrono::{DateTime, Utc};
use flate2::{Compression, read::{DeflateDecoder, ZlibDecoder}, write::DeflateEncoder};
use serde_json::{Value, json};
use std::{collections::HashMap, io::{Read, Write}, net::Ipv4Addr};

pub const MAGIC: &str = "HTBR1:";
const MAX_DECODED_BYTES: u64 = 65_536;

type R<T> = Result<T, String>;

pub fn decode_hytale(code: &str) -> R<Value> {
    let payload = decode_b64(code)?;
    let mut data = None;
    for kind in 0..3 {
        let mut output = Vec::new();
        let result = match kind {
            0 => DeflateDecoder::new(payload.as_slice()).take(MAX_DECODED_BYTES + 1).read_to_end(&mut output),
            1 => ZlibDecoder::new(payload.as_slice()).take(MAX_DECODED_BYTES + 1).read_to_end(&mut output),
            _ => { output = payload.clone(); Ok(output.len()) }
        };
        if result.is_ok() && (output.len() as u64) <= MAX_DECODED_BYTES {
            if let Ok(parsed) = serde_json::from_slice::<Value>(&output) {
                if parsed.get("Candidates").is_some_and(Value::is_array) { data = Some(parsed); break; }
            }
        }
    }
    data.ok_or_else(|| "Invalid Hytale share code (Base64 / raw DEFLATE / JSON)".into())
}

pub fn encode_hytale(value: &Value) -> R<String> {
    let data = serde_json::to_vec(value).map_err(|e| e.to_string())?;
    Ok(STANDARD.encode(deflate(&data)?))
}

fn deflate(bytes: &[u8]) -> R<Vec<u8>> {
    let mut encoder = DeflateEncoder::new(Vec::new(), Compression::best());
    encoder.write_all(bytes).map_err(|e| e.to_string())?;
    encoder.finish().map_err(|e| e.to_string())
}

fn decode_b64(text: &str) -> R<Vec<u8>> {
    if text.len() > 512_000 { return Err("Invite code is too large".into()); }
    let trimmed: String = text.split_whitespace().collect();
    for decoder in [&STANDARD, &STANDARD_NO_PAD, &URL_SAFE, &URL_SAFE_NO_PAD] {
        if let Ok(out) = decoder.decode(trimmed.as_bytes()) { return Ok(out); }
    }
    Err("Invalid Base64 share code".into())
}

fn inflate_raw(bytes: &[u8]) -> R<Vec<u8>> {
    let mut decoded = Vec::new();
    DeflateDecoder::new(bytes).take(MAX_DECODED_BYTES + 1)
        .read_to_end(&mut decoded).map_err(|e| e.to_string())?;
    if decoded.len() as u64 > MAX_DECODED_BYTES { return Err("Invite payload too large".into()); }
    Ok(decoded)
}

pub fn validate_expiry(value: &Value) -> R<()> {
    if let Some(exp) = value.get("ExpiresAt").and_then(Value::as_str) {
        let dt = DateTime::parse_from_rfc3339(exp)
            .map_err(|_| "Invalid Hytale invite expiration".to_string())?;
        if dt.with_timezone(&Utc) <= Utc::now() { return Err("Hytale invite expired. Copy a fresh invite in-game.".into()); }
    }
    Ok(())
}

pub fn choose_port(value: &Value) -> R<u16> {
    let candidates = value.get("Candidates").and_then(Value::as_array)
        .ok_or("Missing Hytale Candidates")?;
    let mut counts: HashMap<u16, usize> = HashMap::new();
    for candidate in candidates {
        if let Some(port) = candidate.get("Port").and_then(Value::as_u64).and_then(|p| u16::try_from(p).ok()).filter(|p| *p > 0) {
            *counts.entry(port).or_default() += 1;
        }
    }
    // Prefer the most common port; break ties in favor of first occurrence.
    let mut best: Option<(u16, usize)> = None;
    for candidate in candidates {
        if let Some(port) = candidate.get("Port").and_then(Value::as_u64)
            .and_then(|p| u16::try_from(p).ok()).filter(|p| *p > 0) {
            let count = counts.get(&port).copied().unwrap_or(0);
            if best.is_none_or(|(_, prev)| count > prev) { best = Some((port, count)); }
        }
    }
    best.map(|(port, _)| port).ok_or_else(|| "Original invite contains no UDP port".into())
}

pub struct BridgeInvite {
    pub hytale: Value,
    pub address: String,
    pub port: u16,
    pub target: Ipv4Addr,
}

pub fn encode_wrapper(hytale: &Value, address: &str, port: u16, target: Ipv4Addr) -> R<String> {
    let wrapper = json!({
        "Version": 1, "TailcatAddress": address, "RemotePort": port,
        "TargetIP": target.to_string(), "Hytale": hytale
    });
    let bytes = serde_json::to_vec(&wrapper).map_err(|e| e.to_string())?;
    Ok(format!("{MAGIC}{}", URL_SAFE_NO_PAD.encode(deflate(&bytes)?)))
}

pub fn decode_wrapper(text: &str) -> R<BridgeInvite> {
    if text.len() > 512_000 { return Err("Bridge invite is too large".into()); }
    let packed = text.trim().strip_prefix(MAGIC).ok_or("Expected HTBR1: bridge invite")?;
    let value: Value = serde_json::from_slice(&inflate_raw(&decode_b64(packed)?)?)
        .map_err(|e| format!("Invalid bridge JSON: {e}"))?;
    if value.get("Version").and_then(Value::as_i64) != Some(1) { return Err("Unsupported bridge invite version".into()); }
    let address = value.get("TailcatAddress").and_then(Value::as_str).ok_or("Missing Tailcat address")?;
    if !address.starts_with("tc") || address.len() > 8192 || address.chars().any(char::is_whitespace) {
        return Err("Invalid Tailcat address".into());
    }
    let port = value.get("RemotePort").and_then(Value::as_u64)
        .and_then(|p| u16::try_from(p).ok()).filter(|p| *p > 0).ok_or("Invalid remote port")?;
    let target = value.get("TargetIP").and_then(Value::as_str).unwrap_or("127.0.0.1")
        .parse::<Ipv4Addr>().map_err(|_| "Invalid IPv4 target in invite")?;
    let hytale = value.get("Hytale").cloned().filter(|v| v.get("Candidates").is_some_and(Value::is_array))
        .ok_or("Missing embedded Hytale invite")?;
    Ok(BridgeInvite { hytale, address: address.to_string(), port, target })
}

pub fn rewrite_local(mut hytale: Value, local_ip: Ipv4Addr, local_port: u16) -> Value {
    if let Some(object) = hytale.as_object_mut() {
        object.remove("Ice");
        object.insert("Candidates".into(), json!([{
            "Type": "Host", "Address": local_ip.to_string(), "Port": local_port, "Priority": 1000
        }]));
    }
    hytale
}

pub fn server_name(data: &Value) -> String {
    data.get("ServerName").and_then(Value::as_str).unwrap_or("Unnamed world").to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    fn sample() -> Value { json!({"ServerName": "Test", "HostUuid": "test", "ExpiresAt":"2099-01-01T00:00:00Z", "Ice":{"SessionId":"abc"}, "Candidates":[{"Type":"Host","Address":"192.168.0.10","Port":5520,"Priority":100}]}) }
    #[test] fn normal_roundtrip() {
        let decoded = decode_hytale(&encode_hytale(&sample()).unwrap()).unwrap();
        assert_eq!(decoded, sample());
        assert_eq!(choose_port(&decoded).unwrap(), 5520);
    }
    #[test] fn wrapper_roundtrip() {
        let x = decode_wrapper(&encode_wrapper(&sample(), "tcTEST", 5520, Ipv4Addr::LOCALHOST).unwrap()).unwrap();
        assert_eq!(x.port, 5520);
        assert_eq!(x.hytale, sample());
        assert_eq!(x.target, Ipv4Addr::LOCALHOST);
        assert!(rewrite_local(x.hytale, Ipv4Addr::new(192, 168, 1, 42), 45678).get("Ice").is_none());
    }
    #[test] fn generated_hytale_code_has_only_the_private_bridge_candidate() {
        let local_ip = Ipv4Addr::new(192, 168, 50, 22);
        let local = rewrite_local(sample(), local_ip, 45678);
        assert!(local.get("Ice").is_none());
        let decoded = decode_hytale(&encode_hytale(&local).unwrap()).unwrap();
        let candidates = decoded["Candidates"].as_array().unwrap();
        assert_eq!(candidates.len(), 1);
        assert_eq!(candidates[0]["Address"], "192.168.50.22");
        assert_eq!(candidates[0]["Port"], 45678);
        assert_ne!(candidates[0]["Address"], "127.0.0.1");
    }
    #[test] fn invalid_rejected() {
        assert!(decode_hytale("invalid-code").is_err());
        assert!(decode_wrapper("HTBR1:aaaa").is_err());
        assert!(decode_hytale(&"a".repeat(512_001)).is_err());
    }
}
