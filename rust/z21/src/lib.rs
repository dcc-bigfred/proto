//! Z21 LAN client protocol.
//!
//! `no_std`, no `alloc`, no sockets. The host (LongFred firmware, or a `std`
//! test) owns UDP.
#![cfg_attr(not(test), no_std)]
#![allow(missing_docs)]

use heapless::Vec;

/// Output buffer the firmware writes onto the UDP socket.
pub const WIRE_BUF_LEN: usize = 256;
pub type WireBuf = Vec<u8, WIRE_BUF_LEN>;

const SERIAL_LEN: usize = 4;
const BCFLAGS_LEN: usize = 8;
const _: () = assert!(WIRE_BUF_LEN >= SERIAL_LEN + BCFLAGS_LEN);

/// Encode / address error.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Error {
    /// `WireBuf` has no remaining capacity.
    BufferFull,
    /// Decoder address 0 is not a valid DCC locomotive address.
    InvalidAddress,
}

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

fn put_lan(out: &mut WireBuf, header: u16, data: &[u8]) -> Result<(), Error> {
    let len = (4 + data.len()) as u16;
    out.extend_from_slice(&len.to_le_bytes())
        .map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(&header.to_le_bytes())
        .map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(data).map_err(|_| Error::BufferFull)?;
    Ok(())
}

fn put_xbus(out: &mut WireBuf, payload: &[u8]) -> Result<(), Error> {
    let mut tmp: Vec<u8, 32> = Vec::new();
    tmp.extend_from_slice(payload)
        .map_err(|_| Error::BufferFull)?;
    tmp.push(xor_sum(payload)).map_err(|_| Error::BufferFull)?;
    put_lan(out, HEADER_XBUS, &tmp)
}

/// LAN_GET_SERIAL_NUMBER.
pub fn encode_get_serial(out: &mut WireBuf) -> Result<(), Error> {
    put_lan(out, HEADER_GET_SERIAL, &[])
}

/// LAN_SET_BROADCASTFLAGS.
pub fn encode_broadcast_flags(out: &mut WireBuf, flags: u32) -> Result<(), Error> {
    put_lan(out, HEADER_SET_BROADCAST, &flags.to_le_bytes())
}

/// LAN_X_SET_LOCO_DRIVE. `steps` is 14/28/128 or proto nibble 0/2/3.
pub fn encode_set_drive(
    out: &mut WireBuf,
    addr: u16,
    speed: u8,
    forward: bool,
    steps: u8,
) -> Result<(), Error> {
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
pub fn encode_set_function(out: &mut WireBuf, addr: u16, func: u8, on: bool) -> Result<(), Error> {
    let (msb, lsb) = addr_bytes(addr);
    let mut type_bits = 0u8;
    if on {
        type_bits = 0x40;
    }
    let db3 = type_bits | (func & 0x3F);
    put_xbus(out, &[0xE4, 0xF8, msb, lsb, db3])
}

/// LAN_X_GET_LOCO_INFO.
pub fn encode_get_loco_info(out: &mut WireBuf, addr: u16) -> Result<(), Error> {
    let (msb, lsb) = addr_bytes(addr);
    put_xbus(out, &[0xE3, 0xF0, msb, lsb])
}

/// LAN_X_SET_TRACK_POWER_*.
pub fn encode_track_power(out: &mut WireBuf, on: bool) -> Result<(), Error> {
    let db0 = if on { 0x81 } else { 0x80 };
    put_xbus(out, &[0x21, db0])
}

/// NMRA CV number (1-based) to the Z21 wire value (`0` = CV1).
#[must_use]
pub fn cv_wire(cv: u16) -> u16 {
    cv.saturating_sub(1)
}

/// LAN_X_CV_READ (§6.1) — programming track, direct mode. `cv` is 1-based.
pub fn encode_cv_read(out: &mut WireBuf, cv: u16) -> Result<(), Error> {
    let w = cv_wire(cv);
    put_xbus(out, &[0x23, 0x11, (w >> 8) as u8, (w & 0xFF) as u8])
}

/// LAN_X_CV_WRITE (§6.2). `cv` is 1-based.
pub fn encode_cv_write(out: &mut WireBuf, cv: u16, value: u8) -> Result<(), Error> {
    let w = cv_wire(cv);
    put_xbus(out, &[0x24, 0x12, (w >> 8) as u8, (w & 0xFF) as u8, value])
}

/// LAN_X_CV_POM_READ_BYTE (§6.8). `cv` is 1-based.
pub fn encode_pom_read(out: &mut WireBuf, addr: u16, cv: u16) -> Result<(), Error> {
    let w = cv_wire(cv);
    let (msb, lsb) = addr_bytes(addr);
    let db3 = 0xE4 | ((w >> 8) & 0x03) as u8;
    put_xbus(out, &[0xE6, 0x30, msb, lsb, db3, (w & 0xFF) as u8, 0x00])
}

/// LAN_X_CV_POM_WRITE_BYTE (§6.6). `cv` is 1-based. No Z21 reply.
pub fn encode_pom_write(out: &mut WireBuf, addr: u16, cv: u16, value: u8) -> Result<(), Error> {
    let w = cv_wire(cv);
    let (msb, lsb) = addr_bytes(addr);
    let db3 = 0xEC | ((w >> 8) & 0x03) as u8;
    put_xbus(out, &[0xE6, 0x30, msb, lsb, db3, (w & 0xFF) as u8, value])
}

/// Walk concatenated Z21 records and return the first CV programming reply.
#[must_use]
pub fn parse_cv_reply(buf: &[u8]) -> Option<Event> {
    let mut pkts: Vec<&[u8], 8> = Vec::new();
    split_datagram(buf, &mut pkts);
    for pkt in pkts {
        if let Some(ev) = parse_cv_record(pkt) {
            return Some(ev);
        }
    }
    None
}

fn parse_cv_record(pkt: &[u8]) -> Option<Event> {
    if pkt.len() < 6 || !valid_frame(pkt) {
        return None;
    }
    let header = u16::from_le_bytes([pkt[2], pkt[3]]);
    if header != HEADER_XBUS {
        return None;
    }
    let d = &pkt[4..];
    if d.len() >= 6 && d[0] == 0x64 && d[1] == 0x14 {
        let wire = (u16::from(d[2]) << 8) | u16::from(d[3]);
        return Some(Event::CvResult {
            cv: wire.saturating_add(1),
            value: d[4],
        });
    }
    if d.len() >= 2 && d[0] == 0x61 && d[1] == 0x13 {
        return Some(Event::CvNack);
    }
    if d.len() >= 2 && d[0] == 0x61 && d[1] == 0x12 {
        return Some(Event::CvNackSc);
    }
    None
}

/// Decode a DCC locomotive address from CV1, CV17, CV18, and CV29.
///
/// The `bool` is `true` when CV29 bit 5 (`0x20`) selects the long address.
#[must_use]
pub fn address_from_cvs(cv1: u8, cv17: u8, cv18: u8, cv29: u8) -> Option<(u16, bool)> {
    if cv29 & 0x20 != 0 {
        let addr = (u16::from(cv17 & 0x3F) << 8) | u16::from(cv18);
        Some((addr, true))
    } else {
        Some((u16::from(cv1), false))
    }
}

/// CV writes that program `addr` into the decoder (CV1 or CV17/18, plus CV29 bit 5).
///
/// Short address: CV1 + clear `0x20` in CV29. Long address: CV17 (`| 0xC0`), CV18, set `0x20`.
pub fn address_cv_writes(addr: u16, cv29: u8) -> Result<Vec<(u16, u8), 3>, Error> {
    if addr == 0 {
        return Err(Error::InvalidAddress);
    }
    let mut out = Vec::new();
    if addr <= 127 {
        let _ = out.push((1, addr as u8));
        let _ = out.push((29, cv29 & !0x20));
    } else {
        let _ = out.push((17, ((addr >> 8) as u8) | 0xC0));
        let _ = out.push((18, addr as u8));
        let _ = out.push((29, cv29 | 0x20));
    }
    Ok(out)
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

/// Parse LAN_X_SET_LOCO_FUNCTION_GROUP. `bits` LSB is function `lo`.
#[must_use]
pub fn parse_set_loco_function_group(pkt: &[u8]) -> Option<(u16, u8, u8, u32)> {
    if pkt.len() < 10 || !valid_frame(pkt) {
        return None;
    }
    let header = u16::from_le_bytes([pkt[2], pkt[3]]);
    if header != HEADER_XBUS || pkt[4] != 0xE4 {
        return None;
    }
    let addr = parse_addr(pkt, 6)?;
    let raw = pkt[8];
    let (lo, hi, bits) = match pkt[5] {
        0x20 => {
            let mut bits = 0u32;
            if raw & 0x10 != 0 {
                bits |= 1;
            }
            for i in 0..4u32 {
                if raw & (1 << i) != 0 {
                    bits |= 1 << (i + 1);
                }
            }
            (0, 4, bits)
        }
        0x21 => (5, 8, u32::from(raw & 0x0F)),
        0x22 => (9, 12, u32::from(raw & 0x0F)),
        0x23 => (13, 20, u32::from(raw)),
        0x28 => (21, 28, u32::from(raw)),
        0x29 => (29, 31, u32::from(raw & 0x07)),
        _ => return None,
    };
    Some((addr, lo, hi, bits))
}

/// Build LAN_X_LOCO_INFO (used by tests / dummy server).
pub fn encode_loco_info(
    out: &mut WireBuf,
    addr: u16,
    speed: u8,
    forward: bool,
    steps: u8,
    functions: u32,
) -> Result<(), Error> {
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
    /// LAN_X_CV_READ. `cv` is 1-based (NMRA).
    CvRead {
        cv: u16,
    },
    /// LAN_X_CV_WRITE. `cv` is 1-based (NMRA).
    CvWrite {
        cv: u16,
        value: u8,
    },
    /// LAN_X_CV_POM_READ_BYTE. `cv` is 1-based (NMRA).
    PomRead {
        addr: u16,
        cv: u16,
    },
    /// LAN_X_CV_POM_WRITE_BYTE. `cv` is 1-based (NMRA).
    PomWrite {
        addr: u16,
        cv: u16,
        value: u8,
    },
}

/// Event decoded from incoming datagrams.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Event {
    LocoInfo(LocoInfo),
    Serial(u32),
    /// LAN_X_CV_RESULT. `cv` is 1-based (NMRA).
    CvResult {
        cv: u16,
        value: u8,
    },
    /// LAN_X_CV_NACK.
    CvNack,
    /// LAN_X_CV_NACK_SC (short circuit on the programming track).
    CvNackSc,
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
    pub fn on_connect(&mut self, out: &mut WireBuf) -> Result<(), Error> {
        out.clear();
        encode_get_serial(out)?;
        encode_broadcast_flags(out, 0x0001_0001)
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
            if let Some(ev) = parse_cv_record(pkt) {
                emit(ev);
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
    pub fn encode(&self, cmd: &Command, out: &mut WireBuf) -> Result<(), Error> {
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
            Command::CvRead { cv } => encode_cv_read(out, cv),
            Command::CvWrite { cv, value } => encode_cv_write(out, cv, value),
            Command::PomRead { addr, cv } => encode_pom_read(out, addr, cv),
            Command::PomWrite { addr, cv, value } => encode_pom_write(out, addr, cv, value),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;
    use std::fs;
    use std::path::PathBuf;

    #[derive(Deserialize)]
    struct Vectors {
        cases: std::vec::Vec<Case>,
    }

    #[derive(Deserialize)]
    struct Case {
        id: std::string::String,
        hex: std::string::String,
        op: std::string::String,
        #[serde(default)]
        fields: serde_json::Value,
    }

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
            let b =
                u8::from_str_radix(core::str::from_utf8(&bytes[i..i + 2]).unwrap(), 16).unwrap();
            out.push(b).unwrap();
            i += 2;
        }
        out
    }

    fn load(rel: &str) -> Vectors {
        let raw = fs::read_to_string(testdata(rel)).expect(rel);
        serde_json::from_str(&raw).expect("json")
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
        for c in load("z21/frames.json").cases {
            let want = decode_hex(&c.hex);
            let mut out = WireBuf::new();
            match c.op.as_str() {
                "get_serial" => encode_get_serial(&mut out).unwrap(),
                "set_drive" if c.id == "set_drive_3_50_fwd_128" => {
                    encode_set_drive(&mut out, 3, 50, true, 3).unwrap();
                }
                "set_drive" if c.id == "set_drive_stop_fwd" => {
                    encode_set_drive(&mut out, 3, 0, true, 3).unwrap();
                }
                "set_function" => encode_set_function(&mut out, 3, 0, true).unwrap(),
                "loco_info" => encode_loco_info(&mut out, 3, 50, true, 128, 1).unwrap(),
                "set_broadcast_flags" => {
                    encode_broadcast_flags(&mut out, 0x0001_0001).unwrap();
                }
                _ => continue,
            }
            assert_eq!(out.as_slice(), want.as_slice(), "{}", c.id);
            if c.op == "loco_info" {
                let info = parse_loco_info(out.as_slice()).expect("parse");
                assert_eq!(info.addr, 3);
                assert_eq!(info.speed, 50);
                assert!(info.forward);
                assert_eq!(info.functions & 1, 1);
            }
        }
    }

    #[test]
    fn function_group_parse() {
        for c in load("z21/function_group.json").cases {
            let pkt = decode_hex(&c.hex);
            let (addr, lo, hi, bits) = parse_set_loco_function_group(pkt.as_slice()).expect(&c.id);
            assert_eq!(addr, c.fields["addr"].as_u64().unwrap() as u16, "{}", c.id);
            assert_eq!(lo, c.fields["lo"].as_u64().unwrap() as u8, "{}", c.id);
            assert_eq!(hi, c.fields["hi"].as_u64().unwrap() as u8, "{}", c.id);
            assert_eq!(bits, c.fields["bits"].as_u64().unwrap() as u32, "{}", c.id);
        }
    }

    #[test]
    fn pom_write_encodes() {
        let mut out = WireBuf::new();
        Client::new()
            .encode(
                &Command::PomWrite {
                    addr: 128,
                    cv: 1,
                    value: 0xAA,
                },
                &mut out,
            )
            .unwrap();
        assert_eq!(out[0], 0x0C);
        assert_eq!(&out[4..6], &[0xE6, 0x30]);
        assert_eq!(out[6], 0xC0);
        assert_eq!(out[7], 0x80);
        assert_eq!(out[8], 0xEC);
        assert_eq!(out[9], 0x00);
        assert_eq!(out[10], 0xAA);
    }

    #[test]
    fn cv_read_matches_wizard() {
        let mut out = WireBuf::new();
        encode_cv_read(&mut out, 1).unwrap();
        assert_eq!(
            out.as_slice(),
            &[0x09, 0x00, 0x40, 0x00, 0x23, 0x11, 0x00, 0x00, 0x32]
        );
    }

    #[test]
    fn cv_write_matches_wizard() {
        let mut out = WireBuf::new();
        encode_cv_write(&mut out, 8, 0x20).unwrap();
        assert_eq!(
            out.as_slice(),
            &[0x0A, 0x00, 0x40, 0x00, 0x24, 0x12, 0x00, 0x07, 0x20, 0x11]
        );
    }

    #[test]
    fn cv_vectors_match_go() {
        let cli = Client::new();
        for c in load("z21/cv.json").cases {
            let want = decode_hex(&c.hex);
            let mut out = WireBuf::new();
            match c.op.as_str() {
                "cv_read" => {
                    let cv = c.fields["cv"].as_u64().unwrap() as u16;
                    cli.encode(&Command::CvRead { cv }, &mut out).unwrap();
                }
                "cv_write" => {
                    let cv = c.fields["cv"].as_u64().unwrap() as u16;
                    let value = c.fields["value"].as_u64().unwrap() as u8;
                    cli.encode(&Command::CvWrite { cv, value }, &mut out)
                        .unwrap();
                }
                "pom_read" => {
                    let addr = c.fields["addr"].as_u64().unwrap() as u16;
                    let cv = c.fields["cv"].as_u64().unwrap() as u16;
                    cli.encode(&Command::PomRead { addr, cv }, &mut out)
                        .unwrap();
                }
                "cv_result" | "cv_nack" | "cv_nack_sc" => {
                    // replies are parsed, not encoded as Command
                }
                _ => panic!("unknown op {}", c.op),
            }
            if matches!(c.op.as_str(), "cv_read" | "cv_write" | "pom_read") {
                assert_eq!(out.as_slice(), want.as_slice(), "{}", c.id);
            }
            match c.op.as_str() {
                "cv_result" => {
                    assert_eq!(
                        parse_cv_reply(want.as_slice()),
                        Some(Event::CvResult { cv: 8, value: 0x20 }),
                        "{}",
                        c.id
                    );
                }
                "cv_nack" => {
                    assert_eq!(
                        parse_cv_reply(want.as_slice()),
                        Some(Event::CvNack),
                        "{}",
                        c.id
                    );
                }
                "cv_nack_sc" => {
                    assert_eq!(
                        parse_cv_reply(want.as_slice()),
                        Some(Event::CvNackSc),
                        "{}",
                        c.id
                    );
                }
                _ => {}
            }
        }
    }

    #[test]
    fn on_bytes_emits_cv_events() {
        let mut cli = Client::new();
        for c in load("z21/cv.json").cases {
            let pkt = decode_hex(&c.hex);
            let mut events: std::vec::Vec<Event> = std::vec::Vec::new();
            cli.on_bytes(pkt.as_slice(), &mut |ev| events.push(ev));
            match c.op.as_str() {
                "cv_result" => {
                    assert_eq!(events, [Event::CvResult { cv: 8, value: 0x20 }], "{}", c.id);
                }
                "cv_nack" => assert_eq!(events, [Event::CvNack], "{}", c.id),
                "cv_nack_sc" => assert_eq!(events, [Event::CvNackSc], "{}", c.id),
                _ => assert!(events.is_empty(), "{} should not emit inbound events", c.id),
            }
        }
    }

    #[test]
    fn address_from_cvs_short_and_long() {
        assert_eq!(address_from_cvs(7, 0, 0, 0x06), Some((7, false)));
        assert_eq!(address_from_cvs(0, 0xC4, 0xD2, 0x26), Some((1234, true)));
    }

    #[test]
    fn address_cv_writes_short_clears_long_bit() {
        let writes = address_cv_writes(7, 0x26).unwrap();
        assert_eq!(writes.as_slice(), &[(1, 7), (29, 0x06)]);
    }

    #[test]
    fn address_cv_writes_long_sets_cv17_18() {
        let writes = address_cv_writes(1234, 0x06).unwrap();
        assert_eq!(writes.as_slice(), &[(17, 0xC4), (18, 0xD2), (29, 0x26)]);
    }

    #[test]
    fn address_cv_writes_rejects_zero() {
        assert_eq!(address_cv_writes(0, 0), Err(Error::InvalidAddress));
    }
}
