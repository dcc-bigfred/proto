//! RailCom (RCN-217) protocol parser.
//!
//! `#![no_std]`, no `alloc`, no sockets, no command-station transport. The host
//! owns the detector or maps a station-specific frame (e.g. Z21
//! `LAN_RAILCOM_DATACHANGED`) onto [`Update`].
//!
//! Memory profile: **strict heapless**. [`Parser`] and [`LocoTelemetryData`] are fixed-size.
//! One [`Parser`] is one decoder (one loco, or one accessory in [`DecoderKind::Stationary`]).
#![cfg_attr(not(test), no_std)]

mod domain;
mod four_of_eight;
mod parser;

pub use domain::{Info1, LocoTelemetryData};
pub use four_of_eight::{decode, encode_payload, Decoded, ACK_CODES, NACK_CODE, PAYLOAD_CODES};
pub use parser::{decode_address, Parser};

/// RailCom cutout channel.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Channel {
    /// Channel 1 — up to 2 encoded bytes (12-bit datagram).
    One,
    /// Channel 2 — up to 6 encoded bytes (36-bit).
    Two,
}

/// MOB (locomotive) vs STAT (accessory). Datagram IDs and CH1 layout differ (RCN-217 Table 4).
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub enum DecoderKind {
    /// Locomotive decoder: CH1 has ID 1/2/3, CH2 uses Table 6 lengths.
    #[default]
    Mobile,
    /// Accessory decoder: CH1 is 12-bit SRQ with **no** identifier; CH2 uses Table 7.
    Stationary,
}

/// Input to [`Parser::ingest`].
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Update {
    /// Already-decoded locomotive address (Z21 LAN, occupancy, …).
    Address(u16),
    /// Already-decoded dynamic variable (ID 7).
    Dyn {
        /// DV sub-index (RCN-217 Table 13).
        subindex: u8,
        /// 8-bit DV value.
        value: u8,
    },
    /// One 4-of-8 encoded data byte from a detector (start/stop bits stripped).
    Encoded {
        /// Cutout channel this byte arrived on.
        channel: Channel,
        /// 4-of-8 code.
        byte: u8,
    },
}

/// One completed datagram (or ACK/NACK).
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Message {
    /// Channel-1/2 ID 1 — ADR high byte (Table 11).
    AdrHigh(u8),
    /// Channel-1/2 ID 2 — ADR low byte (Table 11).
    AdrLow(u8),
    /// Channel-1 ID 3 — Info1 (Table 12).
    Info1(Info1),
    /// ID 7 DYN.
    Dyn {
        /// 8-bit DV value.
        value: u8,
        /// 6-bit sub-index.
        subindex: u8,
    },
    /// STAT channel 1: 12-bit SRQ, no identifier (§6.1).
    Srq {
        /// Bit 11: extended accessory (`true`) vs basic (`false`).
        extended: bool,
        /// Bits 10–0: accessory address.
        address: u16,
    },
    /// 4-of-8 ACK.
    Ack,
    /// 4-of-8 NACK.
    Nack,
}

/// Decode / channel error.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Error {
    /// Byte is not a 4-of-8 payload, ACK, or NACK.
    InvalidCode,
    /// More payload bytes than the channel allows (2 on CH1, 6 on CH2).
    ChannelFull,
    /// Datagram identifier has no length in Tables 6/7 (cannot skip).
    UnsupportedId(u8),
}

impl core::fmt::Display for Error {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match *self {
            Self::InvalidCode => f.write_str("invalid 4-of-8 code"),
            Self::ChannelFull => f.write_str("RailCom channel full"),
            Self::UnsupportedId(id) => write!(f, "unsupported RailCom id {id}"),
        }
    }
}
