//! WiThrottle client codec.
//!
//! `no_std`, no `alloc`, no sockets. The host owns the TCP stream. Full
//! MultiThrottle session logic lands in a later phase.
#![cfg_attr(not(test), no_std)]
#![allow(missing_docs)]

/// True if `line` is a MultiThrottle command (`M…`).
#[must_use]
pub fn is_multithrottle_line(line: &[u8]) -> bool {
    matches!(line.first(), Some(&b'M'))
}

/// Split `payload` on CR/LF; empty segments are skipped.
pub fn split_lines<'a>(payload: &'a [u8], out: &mut heapless::Vec<&'a [u8], 16>) {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn m0a_is_multithrottle() {
        assert!(is_multithrottle_line(b"M0AS3<;>"));
        assert!(!is_multithrottle_line(b"PPA1"));
        assert!(!is_multithrottle_line(b""));
    }

    #[test]
    fn split_crlf() {
        let mut lines: heapless::Vec<&[u8], 16> = heapless::Vec::new();
        split_lines(b"VN2.0\nHTBigFred\r\nHU1234\n", &mut lines);
        assert_eq!(lines.len(), 3);
        assert_eq!(lines[0], b"VN2.0");
        assert_eq!(lines[1], b"HTBigFred");
        assert_eq!(lines[2], b"HU1234");
    }
}
