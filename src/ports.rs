//! Bounded, deterministic port specifications shared by GUI and CLI.
//! Commas and inclusive ranges are supported (e.g. 25565, 25566-25568).

pub const MAX_PORTS: usize = 32;

pub fn parse(spec: &str) -> Result<Vec<u16>, String> {
    let mut ports = Vec::new();
    if spec.trim().is_empty() { return Err("Specify at least one port".into()); }
    for piece in spec.split(',') {
        let piece = piece.trim();
        if piece.is_empty() { return Err("Empty item in port list".into()); }
        let (start, end) = if let Some((a, b)) = piece.split_once('-') {
            (number(a.trim())?, number(b.trim())?)
        } else {
            let n = number(piece)?;
            (n, n)
        };
        if start > end { return Err("Port range must be ascending".into()); }
        if ports.len() + (end - start) as usize + 1 > MAX_PORTS {
            return Err(format!("At most {MAX_PORTS} ports per bridge"));
        }
        ports.extend(start..=end);
    }
    ports.sort_unstable();
    if ports.windows(2).any(|w| w[0] == w[1]) { return Err("Duplicate port in list".into()); }
    Ok(ports)
}

fn number(text: &str) -> Result<u16, String> {
    if text.is_empty() || !text.bytes().all(|c| c.is_ascii_digit()) {
        return Err(format!("Invalid port: {text}"));
    }
    text.parse::<u16>().ok().filter(|n| *n != 0)
        .ok_or_else(|| format!("Port must be 1-65535: {text}"))
}

/// A single preferred join port moves the whole port set by an offset.
/// An equal-length list maps positions explicitly. Empty means identity.
pub fn local_mapping(remote: &[u16], requested: Option<&[u16]>) -> Result<Vec<u16>, String> {
    let Some(requested) = requested else { return Ok(remote.to_vec()); };
    if requested.len() == remote.len() { return Ok(requested.to_vec()); }
    if requested.len() == 1 && !remote.is_empty() {
        let base = i32::from(requested[0]) - i32::from(remote[0]);
        return remote.iter().map(|p| {
            let mapped = i32::from(*p) + base;
            u16::try_from(mapped).ok().filter(|v| *v != 0)
                .ok_or_else(|| "Shifted local port exceeds 1-65535".into())
        }).collect();
    }
    Err("Local port list must contain one starting port or one port per remote port".into())
}

pub fn csv(ports: &[u16]) -> String {
    ports.iter().map(u16::to_string).collect::<Vec<_>>().join(",")
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test] fn parses_lists_and_ranges() {
        assert_eq!(parse("25566, 25567-25568").unwrap(), vec![25566,25567,25568]);
        assert_eq!(parse("8080").unwrap(),vec![8080]);
        assert_eq!(parse("25567,25566").unwrap(),vec![25566,25567]);
    }
    #[test] fn rejects_bad_specs() {
        for bad in ["", "0", "65536", "1-65535", "8-7", "2-2,2", "123,", "-1", "1--2", "7 8"] {
            assert!(parse(bad).is_err(), "accepted {bad}");
        }
    }
    #[test] fn keeps_mapping_shape() {
        let remote = [19132,19133,19135];
        assert_eq!(local_mapping(&remote, Some(&[20000])).unwrap(), [20000,20001,20003]);
        assert_eq!(local_mapping(&remote, Some(&[3000,4000,5000])).unwrap(), [3000,4000,5000]);
        assert!(local_mapping(&remote, Some(&[65535])).is_err());
    }
}
