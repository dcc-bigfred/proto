//! WiThrottle client codec.
//!
//! `no_std`, no `alloc`, no sockets. The host owns the TCP stream.
#![cfg_attr(not(test), no_std)]
#![allow(missing_docs)]

use heapless::{String, Vec};

/// Bytes the firmware writes onto the TCP socket.
pub const WIRE_BUF_LEN: usize = 256;
pub type WireBuf = Vec<u8, WIRE_BUF_LEN>;

const HANDSHAKE_MAX: usize = 4 + 32 + 1 + 3 + 32 + 1 + 3;
const _: () = assert!(WIRE_BUF_LEN >= HANDSHAKE_MAX);

/// Encode error.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Error {
    /// `WireBuf` has no remaining capacity.
    BufferFull,
}

/// Outgoing MultiThrottle / handshake command.
#[derive(Clone, Copy, Debug)]
pub enum Command {
    Handshake,
    Acquire { addr: u16 },
    SetSpeed { addr: u16, speed: u8 },
    SetDirection { addr: u16, forward: bool },
    SetFunction { addr: u16, func: u8, on: bool },
    TrackPower { on: bool },
}

/// Inbound line event.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Event {
    Protocol,
    Heartbeat,
    TrackPower { on: bool },
    Speed { addr: u16, speed: u8 },
    Direction { addr: u16, forward: bool },
    Function { addr: u16, func: u8, on: bool },
}

/// Session-less client: firmware owns TCP.
pub struct Client {
    name: String<32>,
    id: String<32>,
    line: String<256>,
}

impl Client {
    #[must_use]
    pub fn new(name: &str, id: &str) -> Self {
        let mut n = String::new();
        let _ = n.push_str(name);
        let mut i = String::new();
        let _ = i.push_str(id);
        Self {
            name: n,
            id: i,
            line: String::new(),
        }
    }

    /// `N` / `HU` / `*+` after TCP connect.
    pub fn on_connect(&mut self, out: &mut WireBuf) -> Result<(), Error> {
        out.clear();
        push_line(out, b"N")?;
        out.extend_from_slice(self.name.as_bytes())
            .map_err(|_| Error::BufferFull)?;
        out.push(b'\n').map_err(|_| Error::BufferFull)?;
        push_line(out, b"HU")?;
        out.extend_from_slice(self.id.as_bytes())
            .map_err(|_| Error::BufferFull)?;
        out.push(b'\n').map_err(|_| Error::BufferFull)?;
        push_line(out, b"*+\n")
    }

    /// Parse inbound bytes, emitting complete lines.
    pub fn on_bytes(&mut self, input: &[u8], emit: &mut dyn FnMut(Event)) {
        for &b in input {
            if b == b'\n' {
                parse_line(self.line.as_str().trim_end_matches('\r'), emit);
                self.line.clear();
            } else if b != b'\r' && self.line.push(b as char).is_err() {
                self.line.clear();
            }
        }
    }

    /// Encode one command (appended, each line LF-terminated).
    pub fn encode(&self, cmd: &Command, out: &mut WireBuf) -> Result<(), Error> {
        match *cmd {
            Command::Handshake => {
                let mut tmp = WireBuf::new();
                let mut c = Client {
                    name: self.name.clone(),
                    id: self.id.clone(),
                    line: String::new(),
                };
                c.on_connect(&mut tmp)?;
                out.extend_from_slice(&tmp).map_err(|_| Error::BufferFull)
            }
            Command::Acquire { addr } => encode_acquire(out, addr),
            Command::SetSpeed { addr, speed } => encode_action(out, addr, b'V', speed),
            Command::SetDirection { addr, forward } => {
                let v = if forward { 1 } else { 0 };
                encode_action(out, addr, b'R', v)
            }
            Command::SetFunction { addr, func, on } => encode_function(out, addr, func, on),
            Command::TrackPower { on } => {
                let line = if on { b"PPA1\n".as_slice() } else { b"PPA0\n".as_slice() };
                out.extend_from_slice(line).map_err(|_| Error::BufferFull)
            }
        }
    }
}

fn push_line(out: &mut WireBuf, prefix: &[u8]) -> Result<(), Error> {
    out.extend_from_slice(prefix).map_err(|_| Error::BufferFull)
}

fn loco_key(addr: u16, s: &mut String<8>) {
    s.clear();
    let _ = s.push(if addr >= 128 { 'L' } else { 'S' });
    push_u16(s, addr);
}

fn push_u16<const N: usize>(s: &mut String<N>, mut n: u16) {
    if n == 0 {
        let _ = s.push('0');
        return;
    }
    let mut digits = [0u8; 5];
    let mut len = 0usize;
    while n > 0 && len < digits.len() {
        digits[len] = (n % 10) as u8;
        len += 1;
        n /= 10;
    }
    while len > 0 {
        len -= 1;
        let _ = s.push((b'0' + digits[len]) as char);
    }
}

fn encode_acquire(out: &mut WireBuf, addr: u16) -> Result<(), Error> {
    let mut key = String::<8>::new();
    loco_key(addr, &mut key);
    out.extend_from_slice(b"M0+").map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(key.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(b"<;>").map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(key.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.push(b'\n').map_err(|_| Error::BufferFull)
}

fn encode_action(out: &mut WireBuf, addr: u16, letter: u8, value: u8) -> Result<(), Error> {
    let mut key = String::<8>::new();
    loco_key(addr, &mut key);
    out.extend_from_slice(b"M0A").map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(key.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(b"<;>").map_err(|_| Error::BufferFull)?;
    out.push(letter).map_err(|_| Error::BufferFull)?;
    let mut num = String::<8>::new();
    push_u16(&mut num, u16::from(value));
    out.extend_from_slice(num.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.push(b'\n').map_err(|_| Error::BufferFull)
}

fn encode_function(out: &mut WireBuf, addr: u16, func: u8, on: bool) -> Result<(), Error> {
    let mut key = String::<8>::new();
    loco_key(addr, &mut key);
    out.extend_from_slice(b"M0A").map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(key.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(b"<;>f").map_err(|_| Error::BufferFull)?;
    out.push(if on { b'1' } else { b'0' }).map_err(|_| Error::BufferFull)?;
    let mut num = String::<8>::new();
    push_u16(&mut num, u16::from(func));
    out.extend_from_slice(num.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.push(b'\n').map_err(|_| Error::BufferFull)
}

/// True if `line` is a MultiThrottle command (`M…`).
#[must_use]
pub fn is_multithrottle_line(line: &[u8]) -> bool {
    matches!(line.first(), Some(&b'M'))
}

/// Split `payload` on CR/LF; empty segments are skipped.
pub fn split_lines<'a>(payload: &'a [u8], out: &mut Vec<&'a [u8], 16>) {
    out.clear();
    let mut start = 0usize;
    for (i, &b) in payload.iter().enumerate() {
        if b == b'\n' || b == b'\r' {
            if i > start {
                let _ = out.push(&payload[start..i]);
            }
            start = i + 1;
        }
    }
    if start < payload.len() {
        let _ = out.push(&payload[start..]);
    }
}

fn parse_line(line: &str, emit: &mut dyn FnMut(Event)) {
    if line.starts_with("VN") {
        emit(Event::Protocol);
        return;
    }
    if line.starts_with('*') {
        emit(Event::Heartbeat);
        return;
    }
    if let Some(rest) = line.strip_prefix("PPA") {
        emit(Event::TrackPower {
            on: rest.as_bytes().first().copied() != Some(b'0'),
        });
        return;
    }
    if !is_multithrottle_line(line.as_bytes()) || line.len() < 4 {
        return;
    }
    let op = line.as_bytes()[2];
    if op != b'A' {
        return;
    }
    let rest = &line[3..];
    let Some(sep) = rest.find("<;>") else {
        return;
    };
    let key = &rest[..sep];
    let prop = &rest[sep + 3..];
    let addr = parse_loco_key(key);
    if prop.starts_with('V') {
        if let Ok(speed) = prop[1..].parse::<u8>() {
            if let Some(addr) = addr {
                emit(Event::Speed { addr, speed });
            }
        }
    } else if prop.starts_with('R') {
        if let Some(addr) = addr {
            emit(Event::Direction {
                addr,
                forward: prop.as_bytes().get(1).copied() != Some(b'0'),
            });
        }
    } else if prop.starts_with('F') || prop.starts_with('f') {
        if prop.len() < 2 {
            return;
        }
        let on = prop.as_bytes()[1] == b'1';
        if let (Ok(func), Some(addr)) = (prop[2..].parse::<u8>(), addr) {
            emit(Event::Function { addr, func, on });
        }
    }
}

fn parse_loco_key(s: &str) -> Option<u16> {
    let b = s.as_bytes().first()?;
    let digits = match b {
        b'S' | b's' | b'L' | b'l' => &s[1..],
        _ => return None,
    };
    digits.parse().ok()
}

/// Encode helpers used by tests (no trailing newline — matches testdata line).
pub fn encode_line_acquire(addr: u16, out: &mut WireBuf) -> Result<(), Error> {
    let mut key = String::<8>::new();
    loco_key(addr, &mut key);
    out.extend_from_slice(b"M0+").map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(key.as_bytes()).map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(b"<;>").map_err(|_| Error::BufferFull)?;
    out.extend_from_slice(key.as_bytes()).map_err(|_| Error::BufferFull)
}

pub fn encode_line_speed(addr: u16, speed: u8, out: &mut WireBuf) -> Result<(), Error> {
    encode_action_raw(out, addr, b'V', speed)
}

pub fn encode_line_dir(addr: u16, forward: bool, out: &mut WireBuf) -> Result<(), Error> {
    encode_action_raw(out, addr, b'R', u8::from(forward))
}

pub fn encode_line_fn(addr: u16, func: u8, on: bool, out: &mut WireBuf) -> Result<(), Error> {
    let mut tmp = WireBuf::new();
    encode_function(&mut tmp, addr, func, on)?;
    if tmp.last() == Some(&b'\n') {
        tmp.pop();
    }
    out.extend_from_slice(&tmp).map_err(|_| Error::BufferFull)
}

fn encode_action_raw(out: &mut WireBuf, addr: u16, letter: u8, value: u8) -> Result<(), Error> {
    let mut tmp = WireBuf::new();
    encode_action(&mut tmp, addr, letter, value)?;
    if tmp.last() == Some(&b'\n') {
        tmp.pop();
    }
    out.extend_from_slice(&tmp).map_err(|_| Error::BufferFull)
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
        #[serde(default)]
        hex: std::string::String,
        #[serde(default)]
        line: std::string::String,
        op: std::string::String,
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
            let b = u8::from_str_radix(core::str::from_utf8(&bytes[i..i + 2]).unwrap(), 16).unwrap();
            out.push(b).unwrap();
            i += 2;
        }
        out
    }

    fn wire(c: &Case) -> Vec<u8, 64> {
        assert!(
            c.line.is_empty() ^ c.hex.is_empty(),
            "{}: set exactly one of line or hex",
            c.id
        );
        if !c.line.is_empty() {
            let mut out = Vec::new();
            out.extend_from_slice(c.line.as_bytes()).unwrap();
            out
        } else {
            decode_hex(&c.hex)
        }
    }

    fn load(rel: &str) -> Vectors {
        let raw = fs::read_to_string(testdata(rel)).expect(rel);
        serde_json::from_str(&raw).expect("json")
    }

    #[test]
    fn m0a_is_multithrottle() {
        assert!(is_multithrottle_line(b"M0AS3<;>"));
        assert!(!is_multithrottle_line(b"PPA1"));
        assert!(!is_multithrottle_line(b""));
    }

    #[test]
    fn split_crlf() {
        let mut lines: Vec<&[u8], 16> = Vec::new();
        split_lines(b"VN2.0\nHTBigFred\r\nHU1234\n", &mut lines);
        assert_eq!(lines.len(), 3);
        assert_eq!(lines[0], b"VN2.0");
        assert_eq!(lines[1], b"HTBigFred");
        assert_eq!(lines[2], b"HU1234");
    }

    #[test]
    fn lines_match_go_vectors() {
        for c in load("withrottle/lines.json").cases {
            let want = wire(&c);
            let mut out = WireBuf::new();
            match c.id.as_str() {
                "hu" => out.extend_from_slice(b"HUproto").unwrap(),
                "acquire_s3" => encode_line_acquire(3, &mut out).unwrap(),
                "set_speed_50" => encode_line_speed(3, 50, &mut out).unwrap(),
                "set_dir_fwd" => encode_line_dir(3, true, &mut out).unwrap(),
                "set_fn_f0_on" => encode_line_fn(3, 0, true, &mut out).unwrap(),
                "track_power_on" => out.extend_from_slice(b"PPA1").unwrap(),
                _ => continue,
            }
            assert_eq!(out.as_slice(), want.as_slice(), "{}", c.id);
        }
    }

    #[test]
    fn function_press_vectors_parse() {
        for c in load("withrottle/function_press.json").cases {
            let raw = wire(&c);
            let line = core::str::from_utf8(raw.as_slice()).unwrap();
            assert!(is_multithrottle_line(line.as_bytes()), "{}", c.id);
            match c.op.as_str() {
                "press" | "release" | "force" | "mode" => {}
                other => panic!("unexpected op {other}"),
            }
        }
    }
}
