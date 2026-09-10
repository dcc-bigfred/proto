//! 4-of-8 encoding from RCN-217 Table 2.

use crate::Error;

/// ACK 4-of-8 codes: “command understood” / “YES”.
pub const ACK_CODES: [u8; 2] = [0b0000_1111, 0b1111_0000];
/// NACK 4-of-8 code: “command or CV is not supported”.
pub const NACK_CODE: u8 = 0b0011_1100;

/// 4-of-8 code for 6-bit payload `0x00`…`0x3F` (RCN-217 Table 2).
pub const PAYLOAD_CODES: [u8; 64] = [
    0b1010_1100, // 0x00
    0b1010_1010, // 0x01
    0b1010_1001, // 0x02
    0b1010_0101, // 0x03
    0b1010_0011, // 0x04
    0b1010_0110, // 0x05
    0b1001_1100, // 0x06
    0b1001_1010, // 0x07
    0b1001_1001, // 0x08
    0b1001_0101, // 0x09
    0b1001_0011, // 0x0A
    0b1001_0110, // 0x0B
    0b1000_1110, // 0x0C
    0b1000_1101, // 0x0D
    0b1000_1011, // 0x0E
    0b1011_0001, // 0x0F
    0b1011_0010, // 0x10
    0b1011_0100, // 0x11
    0b1011_1000, // 0x12
    0b0111_0100, // 0x13
    0b0111_0010, // 0x14
    0b0110_1100, // 0x15
    0b0110_1010, // 0x16
    0b0110_1001, // 0x17
    0b0110_0101, // 0x18
    0b0110_0011, // 0x19
    0b0110_0110, // 0x1A
    0b0101_1100, // 0x1B
    0b0101_1010, // 0x1C
    0b0101_1001, // 0x1D
    0b0101_0101, // 0x1E
    0b0101_0011, // 0x1F
    0b0101_0110, // 0x20
    0b0100_1110, // 0x21
    0b0100_1101, // 0x22
    0b0100_1011, // 0x23
    0b0100_0111, // 0x24
    0b0111_0001, // 0x25
    0b1110_1000, // 0x26
    0b1110_0100, // 0x27
    0b1110_0010, // 0x28
    0b1101_0001, // 0x29
    0b1100_1001, // 0x2A
    0b1100_0101, // 0x2B
    0b1101_1000, // 0x2C
    0b1101_0100, // 0x2D
    0b1101_0010, // 0x2E
    0b1100_1010, // 0x2F
    0b1100_0110, // 0x30
    0b1100_1100, // 0x31
    0b0111_1000, // 0x32
    0b0001_0111, // 0x33
    0b0001_1011, // 0x34
    0b0001_1101, // 0x35
    0b0001_1110, // 0x36
    0b0010_1110, // 0x37
    0b0011_0110, // 0x38
    0b0011_1010, // 0x39
    0b0010_0111, // 0x3A
    0b0010_1011, // 0x3B
    0b0010_1101, // 0x3C
    0b0011_0101, // 0x3D
    0b0011_1001, // 0x3E
    0b0011_0011, // 0x3F
];

const ACK_SENTINEL: i8 = -2;
const NACK_SENTINEL: i8 = -3;

const fn decode_table() -> [i8; 256] {
    let mut t = [-1i8; 256];
    let mut i = 0;
    while i < 64 {
        t[PAYLOAD_CODES[i] as usize] = i as i8;
        i += 1;
    }
    t[ACK_CODES[0] as usize] = ACK_SENTINEL;
    t[ACK_CODES[1] as usize] = ACK_SENTINEL;
    t[NACK_CODE as usize] = NACK_SENTINEL;
    t
}

const DECODE: [i8; 256] = decode_table();

/// One 4-of-8 byte after Hamming-weight-4 decode.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Decoded {
    /// 6 payload bits (`0..=63`).
    Payload(u8),
    /// ACK (`0x0F` or `0xF0`).
    Ack,
    /// NACK (`0x3C`).
    Nack,
}

/// Decode one 4-of-8 code byte (the 8 data bits; start/stop are stripped).
#[must_use]
pub fn decode(code: u8) -> Result<Decoded, Error> {
    match DECODE[code as usize] {
        ACK_SENTINEL => Ok(Decoded::Ack),
        NACK_SENTINEL => Ok(Decoded::Nack),
        v if v >= 0 => Ok(Decoded::Payload(v as u8)),
        _ => Err(Error::InvalidCode),
    }
}

/// Encode 6 payload bits into a 4-of-8 code. `None` when `value >= 64`.
#[must_use]
pub fn encode_payload(value: u8) -> Option<u8> {
    PAYLOAD_CODES.get(value as usize).copied()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn table_roundtrip() {
        for v in 0u8..64 {
            let code = encode_payload(v).unwrap();
            assert_eq!(code.count_ones(), 4, "value {v:#02x} code {code:#08b}");
            assert_eq!(decode(code), Ok(Decoded::Payload(v)));
        }
    }

    #[test]
    fn ack_nack() {
        assert_eq!(decode(0x0F), Ok(Decoded::Ack));
        assert_eq!(decode(0xF0), Ok(Decoded::Ack));
        assert_eq!(decode(NACK_CODE), Ok(Decoded::Nack));
    }

    #[test]
    fn invalid_rejected() {
        assert_eq!(decode(0x00), Err(Error::InvalidCode));
        assert_eq!(decode(0xFF), Err(Error::InvalidCode));
        // reserved Hamming-weight-4 codes
        assert_eq!(decode(0b1110_0001), Err(Error::InvalidCode));
        assert_eq!(decode(0b1100_0011), Err(Error::InvalidCode));
        assert_eq!(decode(0b1000_0111), Err(Error::InvalidCode));
    }
}
