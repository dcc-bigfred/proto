//! Z21 LAN client codec.
//!
//! `no_std`, no `alloc`, no sockets. The host (LongFred firmware, or a `std`
//! test) owns UDP.
#![cfg_attr(not(test), no_std)]
#![allow(missing_docs)]

use heapless::Vec;

/// Output buffer the firmware writes onto the UDP socket.
pub type WireBuf = Vec<u8, 256>;

/// LAN headers (little-endian uint16 at bytes 2–3).
pub const HEADER_GET_SERIAL: u16 = 0x0010;
pub const HEADER_XBUS: u16 = 0x0040;
pub const HEADER_SET_BROADCAST: u16 = 0x0050;

/// XOR of all bytes. X-Bus trailing checksum is XOR so the payload plus
/// checksum sums to 0.
#[must_use]
pub fn xor_sum(bytes: &[u8]) -> u8 {
    let mut x = 0u8;
    for &b in bytes {
        x ^= b;
    }
    x
}

/// True when `pkt` is a well-formed length-prefixed Z21 LAN dataset.
#[must_use]
pub fn valid_frame(pkt: &[u8]) -> bool {
    if pkt.len() < 4 {
        return false;
    }
    let len = u16::from_le_bytes([pkt[0], pkt[1]]) as usize;
    len == pkt.len() && len >= 4
}

/// Split a UDP payload into length-prefixed datasets.
pub fn split_datagram<'a>(mut b: &'a [u8], out: &mut Vec<&'a [u8], 8>) {
    out.clear();
    while b.len() >= 4 {
        let l = u16::from_le_bytes([b[0], b[1]]) as usize;
        if l < 4 || l > b.len() {
            break;
        }
        let _ = out.push(&b[..l]);
        b = &b[l..];
    }
}

/// DCC address → Adr_MSB / Adr_LSB.
#[must_use]
pub fn addr_bytes(addr: u16) -> (u8, u8) {
    let mut msb = ((addr >> 8) & 0x3F) as u8;
    if addr >= 128 {
        msb |= 0xC0;
    }
    (msb, (addr & 0xFF) as u8)
}

#[must_use]
pub fn parse_addr(pkt: &[u8], offset: usize) -> Option<u16> {
    let msb = *pkt.get(offset)?;
    let lsb = *pkt.get(offset + 1)?;
    Some((u16::from(msb & 0x3F) << 8) | u16::from(lsb))
}

/// DB3 (RVVVVVVV) for LAN_X_SET_LOCO_DRIVE. Matches Go `EncodeDriveDB3`.
/// `speed_steps` is the proto nibble: 0=14, 2=28, 3=128.
#[must_use]
pub fn encode_drive_db3(mut speed: u8, forward: bool, speed_steps: u8) -> u8 {
    let db3 = if forward { 0x80 } else { 0 };
    match speed {
        0 => return db3,
        1 => return db3 | 0x01,
        _ => {}
    }
    match speed_steps {
        0 => {
            if speed > 15 {
                speed = 15;
            }
            db3 | (speed & 0x0F)
        }
        2 => {
            if speed > 28 {
                speed = 28;
            }
            let speed_bits = (speed + 3) / 2;
            let speed_bit5 = (speed + 3) % 2;
            db3 | (speed_bit5 << 4) | (speed_bits & 0x0F)
        }
        _ => {
            if speed > 127 {
                speed = 127;
            }
            db3 | (speed & 0x7F)
        }
    }
}

/// Decode DB2/DB3 from LAN_X_LOCO_INFO. Matches Go `DecodeDriveFromLocoInfo`.
#[must_use]
pub fn decode_drive_from_loco_info(db2: u8, db3: u8) -> (u8, bool) {
    let forward = db3 & 0x80 != 0;
    let v = db3 & 0x7F;
    match db2 & 0x07 {
        0 => (v & 0x0F, forward),
        2 => {
            let speed_bits = v & 0x0F;
            let speed_bit5 = (v >> 4) & 0x01;
            let raw = i32::from(speed_bits) * 2 + i32::from(speed_bit5);
            let speed = match raw {
                0 | 1 => 0,
                2 | 3 => 1,
                r => (r - 3) as u8,
            };
            (speed, forward)
        }
        _ => (v, forward),
    }
}

fn put_lan(out: &mut WireBuf, header: u16, data: &[u8]) -> Result<(), ()> {
    let len = (4 + data.len()) as u16;
    out.extend_from_slice(&len.to_le_bytes()).map_err(|_| ())?;
    out.extend_from_slice(&header.to_le_bytes()).map_err(|_| ())?;
    out.extend_from_slice(data).map_err(|_| ())?;
    Ok(())
}

fn put_xbus(out: &mut WireBuf, payload: &[u8]) -> Result<(), ()> {
    let mut tmp: Vec<u8, 32> = Vec::new();
    tmp.extend_from_slice(payload).map_err(|_| ())?;
    tmp.push(xor_sum(payload)).map_err(|_| ())?;
    put_lan(out, HEADER_XBUS, &tmp)
}

/// LAN_GET_SERIAL_NUMBER.
pub fn encode_get_serial(out: &mut WireBuf) -> Result<(), ()> {
    put_lan(out, HEADER_GET_SERIAL, &[])
}

/// LAN_SET_BROADCASTFLAGS.
pub fn encode_broadcast_flags(out: &mut WireBuf, flags: u32) -> Result<(), ()> {
    put_lan(out, HEADER_SET_BROADCAST, &flags.to_le_bytes())
}

/// LAN_X_SET_LOCO_DRIVE. `steps` is 14/28/128 or proto nibble 0/2/3.
pub fn encode_set_drive(
    out: &mut WireBuf,
    addr: u16,
    speed: u8,
    forward: bool,
    steps: u8,
) -> Result<(), ()> {
    let (msb, lsb) = addr_bytes(addr);
    let mut s = steps & 0x0F;
    if s == 4 || steps == 128 {
        s = 3;
    } else if steps == 14 {
        s = 0;
    } else if steps == 28 {
        s = 2;
    }
    let db0 = 0x10 | s;
    let db3 = encode_drive_db3(speed, forward, s);
    put_xbus(out, &[0xE4, db0, msb, lsb, db3])
}

/// LAN_X_SET_LOCO_FUNCTION.
pub fn encode_set_function(out: &mut WireBuf, addr: u16, func: u8, on: bool) -> Result<(), ()> {
    let (msb, lsb) = addr_bytes(addr);
    let mut type_bits = 0u8;
    if on {
        type_bits = 0x40;
    }
    let db3 = type_bits | (func & 0x3F);
    put_xbus(out, &[0xE4, 0xF8, msb, lsb, db3])
}

/// LAN_X_GET_LOCO_INFO.
pub fn encode_get_loco_info(out: &mut WireBuf, addr: u16) -> Result<(), ()> {
    let (msb, lsb) = addr_bytes(addr);
    put_xbus(out, &[0xE3, 0xF0, msb, lsb])
}

/// LAN_X_SET_TRACK_POWER_*.
pub fn encode_track_power(out: &mut WireBuf, on: bool) -> Result<(), ()> {
    let db0 = if on { 0x81 } else { 0x80 };
    put_xbus(out, &[0x21, db0])
}

fn encode_function_bytes(mask: u32) -> [u8; 5] {
    let mut b = [0u8; 5];
    if mask & 1 != 0 {
        b[0] |= 0x10;
    }
    for i in 1..=4 {
        if mask & (1 << i) != 0 {
            b[0] |= 1 << (i - 1);
        }
    }
    for i in 5..=12 {
        if mask & (1 << i) != 0 {
            b[1] |= 1 << (i - 5);
        }
    }
    for i in 13..=20 {
        if mask & (1 << i) != 0 {
            b[2] |= 1 << (i - 13);
        }
    }
    for i in 21..=28 {
        if mask & (1 << i) != 0 {
            b[3] |= 1 << (i - 21);
        }
    }
    for i in 29..=31 {
        if mask & (1 << i) != 0 {
            b[4] |= 1 << (i - 29);
        }
    }
    b
}

fn decode_function_bytes(db: &[u8]) -> u32 {
    let mut f = 0u32;
    if let Some(&b0) = db.first() {
        if b0 & 0x10 != 0 {
            f |= 1;
        }
        for i in 0..4 {
            if b0 & (1 << i) != 0 {
                f |= 1 << (i + 1);
            }
        }
    }
    if let Some(&b1) = db.get(1) {
        for i in 0..8 {
            if b1 & (1 << i) != 0 {
                f |= 1 << (i + 5);
            }
        }
    }
    if let Some(&b2) = db.get(2) {
        for i in 0..8 {
            if b2 & (1 << i) != 0 {
                f |= 1 << (i + 13);
            }
        }
    }
    if let Some(&b3) = db.get(3) {
        for i in 0..8 {
            if b3 & (1 << i) != 0 {
                f |= 1 << (i + 21);
            }
        }
    }
    if let Some(&b4) = db.get(4) {
        for i in 0..3 {
            if b4 & (1 << i) != 0 {
                f |= 1 << (i + 29);
            }
        }
    }
    f
}

/// Decoded LAN_X_LOCO_INFO.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct LocoInfo {
    pub addr: u16,
    pub speed: u8,
    pub forward: bool,
    pub functions: u32,
}

/// Parse a complete LAN_X_LOCO_INFO packet.
#[must_use]
pub fn parse_loco_info(pkt: &[u8]) -> Option<LocoInfo> {
    if pkt.len() < 10 || !valid_frame(pkt) {
        return None;
    }
    let header = u16::from_le_bytes([pkt[2], pkt[3]]);
    if header != HEADER_XBUS || pkt[4] != 0xEF {
        return None;
    }
    let addr = parse_addr(pkt, 5)?;
    let (speed, forward) = decode_drive_from_loco_info(pkt[7], pkt[8]);
    let functions = if pkt.len() > 9 {
        decode_function_bytes(&pkt[9..pkt.len().saturating_sub(1)])
    } else {
        0
    };
    Some(LocoInfo {
        addr,
        speed,
        forward,
        functions,
    })
}

/// Build LAN_X_LOCO_INFO (used by tests / dummy server).
pub fn encode_loco_info(
    out: &mut WireBuf,
    addr: u16,
    speed: u8,
    forward: bool,
    steps: u8,
    functions: u32,
) -> Result<(), ()> {
    let (msb, lsb) = addr_bytes(addr);
    let db2 = match steps {
        14 | 0 => 0,
        28 | 2 => 2,
        _ => 4,
    };
    let nibble = match steps {
        14 | 0 => 0,
        28 | 2 => 2,
        _ => 3,
    };
    let db3 = encode_drive_db3(speed, forward, nibble);
    let f = encode_function_bytes(functions);
    put_xbus(
        out,
        &[0xEF, msb, lsb, db2, db3, f[0], f[1], f[2], f[3], f[4]],
    )
}

/// Drive command the firmware encodes into a UDP datagram.
#[derive(Clone, Copy, Debug)]
pub enum Command {
    SetSpeed {
        addr: u16,
        speed: u8,
        forward: bool,
        steps: u8,
    },
    SetFunction {
        addr: u16,
        func: u8,
        on: bool,
    },
    GetLocoInfo {
        addr: u16,
    },
    TrackPower {
        on: bool,
    },
}

/// Event decoded from incoming datagrams.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Event {
    LocoInfo(LocoInfo),
    Serial(u32),
}

/// Session-less client: firmware owns UDP.
#[derive(Default)]
pub struct Client;

impl Client {
    #[must_use]
    pub fn new() -> Self {
        Self
    }

    /// Bytes to send after the UDP socket is ready (serial probe + broadcast flags).
    pub fn on_connect(&mut self, out: &mut WireBuf) {
        out.clear();
        let _ = encode_get_serial(out);
        let _ = encode_broadcast_flags(out, 0x0001_0001);
    }

    /// Parse inbound datagrams.
    pub fn on_bytes(&mut self, input: &[u8], emit: &mut dyn FnMut(Event)) {
        let mut pkts: Vec<&[u8], 8> = Vec::new();
        split_datagram(input, &mut pkts);
        for pkt in pkts {
            if let Some(info) = parse_loco_info(pkt) {
                emit(Event::LocoInfo(info));
                continue;
            }
            if valid_frame(pkt) && pkt.len() >= 8 {
                let header = u16::from_le_bytes([pkt[2], pkt[3]]);
                if header == HEADER_GET_SERIAL {
                    let serial = u32::from_le_bytes([pkt[4], pkt[5], pkt[6], pkt[7]]);
                    emit(Event::Serial(serial));
                }
            }
        }
    }

    /// Encode one command into `out` (appended).
    pub fn encode(&self, cmd: &Command, out: &mut WireBuf) -> Result<(), ()> {
        match *cmd {
            Command::SetSpeed {
                addr,
                speed,
                forward,
                steps,
            } => encode_set_drive(out, addr, speed, forward, steps),
            Command::SetFunction { addr, func, on } => encode_set_function(out, addr, func, on),
            Command::GetLocoInfo { addr } => encode_get_loco_info(out, addr),
            Command::TrackPower { on } => encode_track_power(out, on),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::path::PathBuf;

    fn testdata(rel: &str) -> PathBuf {
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../..")
            .join("testdata")
            .join(rel)
    }

    fn decode_hex(s: &str) -> Vec<u8, 64> {
        let mut out = Vec::new();
        let bytes = s.as_bytes();
        let mut i = 0;
        while i + 1 < bytes.len() {
            let b = u8::from_str_radix(core::str::from_utf8(&bytes[i..i + 2]).unwrap(), 16).unwrap();
            out.push(b).unwrap();
            i += 2;
        }
        out
    }

    #[test]
    fn xor_sum_xbus_example() {
        assert_eq!(xor_sum(&[0x21, 0x24]), 0x05);
    }

    #[test]
    fn stop_keeps_r() {
        assert_eq!(encode_drive_db3(0, true, 3), 0x80);
    }

    #[test]
    fn frames_match_go_vectors() {
        let raw = fs::read_to_string(testdata("z21/frames.json")).expect("frames.json");
        for (id, hex, op) in parse_cases(&raw) {
            let want = decode_hex(&hex);
            let mut out = WireBuf::new();
            match op.as_str() {
                "get_serial" => encode_get_serial(&mut out).unwrap(),
                "set_drive" if id == "set_drive_3_50_fwd_128" => {
                    encode_set_drive(&mut out, 3, 50, true, 3).unwrap();
                }
                "set_drive" if id == "set_drive_stop_fwd" => {
                    encode_set_drive(&mut out, 3, 0, true, 3).unwrap();
                }
                "set_function" => encode_set_function(&mut out, 3, 0, true).unwrap(),
                "loco_info" => encode_loco_info(&mut out, 3, 50, true, 128, 1).unwrap(),
                "set_broadcast_flags" => {
                    encode_broadcast_flags(&mut out, 0x0001_0001).unwrap();
                }
                _ => continue,
            }
            assert_eq!(out.as_slice(), want.as_slice(), "{id}");
            if op == "loco_info" {
                let info = parse_loco_info(out.as_slice()).expect("parse");
                assert_eq!(info.addr, 3);
                assert_eq!(info.speed, 50);
                assert!(info.forward);
                assert_eq!(info.functions & 1, 1);
            }
        }
    }

    fn parse_cases(json: &str) -> Vec<(std::string::String, std::string::String, std::string::String), 16> {
        let mut out = Vec::new();
        for obj in json.split('{') {
            if !obj.contains("\"hex\"") {
                continue;
            }
            let Some(id) = json_field(obj, "id") else {
                continue;
            };
            let Some(hex) = json_field(obj, "hex") else {
                continue;
            };
            let Some(op) = json_field(obj, "op") else {
                continue;
            };
            let _ = out.push((id, hex, op));
        }
        out
    }

    fn json_field(obj: &str, key: &str) -> Option<std::string::String> {
        let pat = format!("\"{key}\": \"");
        let i = obj.find(&pat)?;
        let rest = &obj[i + pat.len()..];
        let end = rest.find('"')?;
        Some(rest[..end].to_string())
    }
}
