//! Z21 LAN client codec.
//!
//! `no_std`, no `alloc`, no sockets. The host (LongFred firmware, or a `std`
//! test) owns UDP. Full encode/decode lands in a later phase.
#![cfg_attr(not(test), no_std)]
#![allow(missing_docs)]

/// XOR of all bytes. Z21 X-Bus trailing checksum.
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn xor_sum_empty() {
        assert_eq!(xor_sum(&[]), 0);
    }

    #[test]
    fn xor_sum_xbus_example() {
        // LAN_X_GET_STATUS payload without checksum: 0x21 0x24 → chk 0x05
        assert_eq!(xor_sum(&[0x21, 0x24]), 0x05);
    }

    #[test]
    fn valid_frame_serial_number_req() {
        let pkt = [0x04, 0x00, 0x10, 0x00];
        assert!(valid_frame(&pkt));
    }

    #[test]
    fn valid_frame_rejects_short() {
        assert!(!valid_frame(&[0x04, 0x00, 0x10]));
    }
}
