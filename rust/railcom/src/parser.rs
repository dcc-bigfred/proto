//! Stateful RailCom parser: 4-of-8 datagrams and already-decoded updates.

use crate::four_of_eight::{decode, Decoded};
use crate::{Channel, DecoderKind, Error, Info1, LocoTelemetryData, Message, Update};

const CH1_MAX: u8 = 2;
const CH2_MAX: u8 = 6;

/// Assembles ADR (ID 1+2) and DYN (including speed DV 0/1) into [`LocoTelemetryData`].
///
/// One instance is one decoder. After ACK/NACK on a channel, further bytes on
/// that channel are ignored until [`Parser::begin_cutout`] (RCN-217 §3.1).
#[derive(Clone, Debug)]
pub struct Parser {
    kind: DecoderKind,
    data: LocoTelemetryData,
    adr_high: Option<u8>,
    adr_low: Option<u8>,
    adr_high_fresh: bool,
    adr_low_fresh: bool,
    dv20_lo: u8,
    dv20_hi_pending: bool,
    ch1: ChannelBuf,
    ch2: ChannelBuf,
    ch1_ignore: bool,
    ch2_ignore: bool,
}

impl Default for Parser {
    fn default() -> Self {
        Self::new()
    }
}

#[derive(Clone, Copy, Debug, Default)]
struct ChannelBuf {
    units: [u8; 6],
    len: u8,
}

impl ChannelBuf {
    fn push(&mut self, unit: u8, max: u8) -> Result<(), Error> {
        if self.len >= max {
            return Err(Error::ChannelFull);
        }
        self.units[self.len as usize] = unit;
        self.len += 1;
        Ok(())
    }

    fn take(&mut self, n: u8) -> [u8; 6] {
        let n_us = n as usize;
        let mut out = [0u8; 6];
        out[..n_us].copy_from_slice(&self.units[..n_us]);
        let rest = self.len - n;
        if rest > 0 {
            self.units.copy_within(n_us..self.len as usize, 0);
        }
        self.len = rest;
        out
    }

    fn clear(&mut self) {
        self.len = 0;
    }
}

impl Parser {
    /// Mobile decoder, empty snapshot, empty channel buffers.
    #[must_use]
    pub fn new() -> Self {
        Self {
            kind: DecoderKind::Mobile,
            data: LocoTelemetryData::default(),
            adr_high: None,
            adr_low: None,
            adr_high_fresh: false,
            adr_low_fresh: false,
            dv20_lo: 0,
            dv20_hi_pending: false,
            ch1: ChannelBuf::default(),
            ch2: ChannelBuf::default(),
            ch1_ignore: false,
            ch2_ignore: false,
        }
    }

    /// Accessory decoder: CH1 is SRQ without an identifier (RCN-217 §6.1).
    #[must_use]
    pub fn stationary() -> Self {
        let mut p = Self::new();
        p.kind = DecoderKind::Stationary;
        p
    }

    /// MOB vs STAT. Set by [`Self::new`] / [`Self::stationary`].
    #[must_use]
    pub fn kind(&self) -> DecoderKind {
        self.kind
    }

    /// Current assembled telemetry.
    #[must_use]
    pub fn snapshot(&self) -> LocoTelemetryData {
        self.data
    }

    /// Drop in-progress 6-bit units and ACK/NACK ignore flags (start of each cutout).
    ///
    /// Does **not** clear ADR halves: ID 1 and ID 2 are sent in alternate cutouts.
    pub fn begin_cutout(&mut self) {
        self.ch1.clear();
        self.ch2.clear();
        self.ch1_ignore = false;
        self.ch2_ignore = false;
        self.dv20_hi_pending = false;
    }

    /// Ingest one update. Returns a datagram [`Message`] when one completes.
    pub fn ingest(&mut self, update: Update) -> Result<Option<Message>, Error> {
        match update {
            Update::Address(addr) => {
                self.data.address = Some(addr);
                self.adr_high = None;
                self.adr_low = None;
                self.adr_high_fresh = false;
                self.adr_low_fresh = false;
                Ok(None)
            }
            Update::Dyn { subindex, value } => {
                self.apply_dyn(value, subindex);
                Ok(Some(Message::Dyn { value, subindex }))
            }
            Update::Encoded { channel, byte } => self.ingest_encoded(channel, byte),
        }
    }

    fn ignore(&self, channel: Channel) -> bool {
        match channel {
            Channel::One => self.ch1_ignore,
            Channel::Two => self.ch2_ignore,
        }
    }

    fn set_ignore(&mut self, channel: Channel) {
        match channel {
            Channel::One => self.ch1_ignore = true,
            Channel::Two => self.ch2_ignore = true,
        }
    }

    fn ingest_encoded(&mut self, channel: Channel, byte: u8) -> Result<Option<Message>, Error> {
        if self.ignore(channel) {
            return Ok(None);
        }
        match decode(byte)? {
            Decoded::Ack => {
                self.buf_mut(channel).clear();
                self.set_ignore(channel);
                Ok(Some(Message::Ack))
            }
            Decoded::Nack => {
                self.buf_mut(channel).clear();
                self.set_ignore(channel);
                Ok(Some(Message::Nack))
            }
            Decoded::Payload(unit) => {
                let max = channel_max(channel);
                if let Err(e) = self.buf_mut(channel).push(unit, max) {
                    self.buf_mut(channel).clear();
                    return Err(e);
                }
                self.try_extract(channel)
            }
        }
    }

    fn try_extract(&mut self, channel: Channel) -> Result<Option<Message>, Error> {
        let len = self.buf(channel).len;
        if len == 0 {
            return Ok(None);
        }
        if self.kind == DecoderKind::Stationary && channel == Channel::One {
            if len < 2 {
                return Ok(None);
            }
            let units = self.buf_mut(channel).take(2);
            return Ok(Some(decode_srq(&units[..2])));
        }
        let id = self.buf(channel).units[0] >> 2;
        let Some(need) = datagram_units(self.kind, channel, id) else {
            self.buf_mut(channel).clear();
            return Err(Error::UnsupportedId(id));
        };
        if len < need {
            return Ok(None);
        }
        let units = self.buf_mut(channel).take(need);
        Ok(self.apply_datagram(id, &units[..need as usize]))
    }

    fn apply_datagram(&mut self, id: u8, units: &[u8]) -> Option<Message> {
        let payload = payload_bits(units);
        match id {
            1 => {
                let high = payload as u8;
                self.adr_high = Some(high);
                self.adr_high_fresh = true;
                if self.adr_low_fresh {
                    self.commit_address();
                }
                Some(Message::AdrHigh(high))
            }
            2 => {
                let low = payload as u8;
                self.adr_low = Some(low);
                self.adr_low_fresh = true;
                if self.adr_high_fresh {
                    self.commit_address();
                }
                Some(Message::AdrLow(low))
            }
            3 if self.kind == DecoderKind::Mobile && units.len() == 2 => {
                let info = Info1::from_bits(payload as u8);
                self.data.info1 = Some(info);
                Some(Message::Info1(info))
            }
            7 => {
                let value = (payload >> 6) as u8;
                let subindex = (payload & 0x3F) as u8;
                self.apply_dyn(value, subindex);
                Some(Message::Dyn { value, subindex })
            }
            _ => None,
        }
    }

    fn apply_dyn(&mut self, value: u8, subindex: u8) {
        match subindex {
            0 => self.data.speed_kmh = Some(u16::from(value)),
            1 => self.data.speed_kmh = Some(256 + u16::from(value)),
            2 => {
                if value & 0x80 == 0 {
                    self.data.load = Some(value & 0x7F);
                } else {
                    self.data.speed_128 = Some(value & 0x7F);
                }
            }
            7 => self.data.qos_percent = Some(value),
            8..=19 => self.data.tanks[(subindex - 8) as usize] = Some(value),
            20 => {
                if !self.dv20_hi_pending {
                    self.dv20_lo = value;
                    self.dv20_hi_pending = true;
                    self.data.location_address = Some(u16::from(value));
                } else {
                    self.data.location_address =
                        Some((u16::from(value & 0x07) << 8) | u16::from(self.dv20_lo));
                    self.dv20_hi_pending = false;
                }
            }
            21 => self.data.warning = Some(value),
            26 => self.data.temperature_c = Some(i16::from(value) - 50),
            46 => self.data.track_voltage_mv = Some(5000 + u16::from(value) * 100),
            _ => {}
        }
    }

    fn commit_address(&mut self) {
        if let (Some(high), Some(low)) = (self.adr_high, self.adr_low) {
            self.data.address = Some(decode_address(high, low));
            self.adr_high_fresh = false;
            self.adr_low_fresh = false;
        }
    }

    fn buf(&self, channel: Channel) -> &ChannelBuf {
        match channel {
            Channel::One => &self.ch1,
            Channel::Two => &self.ch2,
        }
    }

    fn buf_mut(&mut self, channel: Channel) -> &mut ChannelBuf {
        match channel {
            Channel::One => &mut self.ch1,
            Channel::Two => &mut self.ch2,
        }
    }
}

fn channel_max(channel: Channel) -> u8 {
    match channel {
        Channel::One => CH1_MAX,
        Channel::Two => CH2_MAX,
    }
}

fn decode_srq(units: &[u8]) -> Message {
    let bits = (u16::from(units[0]) << 6) | u16::from(units[1]);
    Message::Srq {
        extended: bits & (1 << 11) != 0,
        address: bits & 0x07FF,
    }
}

/// Number of 6-bit units for an identifier, or `None` if length is unknown.
///
/// Channel 1 is always 12 bits (RCN-217 §3), so unknown MOB CH1 IDs are skipped
/// as 2 units instead of desynchronising the rest of the cutout.
fn datagram_units(kind: DecoderKind, channel: Channel, id: u8) -> Option<u8> {
    match kind {
        DecoderKind::Mobile => match (channel, id) {
            (_, 0 | 1 | 2) => Some(2),
            (Channel::One, 3) => Some(2), // info1
            (Channel::One, _) => Some(2), // CH1 is always 12-bit
            (Channel::Two, 3) => Some(3), // ext
            (Channel::Two, 4 | 8 | 9 | 10 | 11 | 12 | 13) => Some(6),
            (Channel::Two, 7) => Some(3),  // dyn
            (Channel::Two, 14) => Some(2), // zeit
            _ => None,
        },
        DecoderKind::Stationary => match (channel, id) {
            (Channel::Two, 0 | 4 | 5 | 6) => Some(2),
            // Table 7 lists STAT4 (ID 3) as 36-bit; §6.4 and the 26.11.2023
            // history make it one data byte (12-bit datagram).
            (Channel::Two, 3) => Some(2),
            (Channel::Two, 7) => Some(3),
            (Channel::Two, 8 | 9 | 10 | 11 | 13) => Some(6),
            _ => None,
        },
    }
}

fn payload_bits(units: &[u8]) -> u32 {
    let mut payload = u32::from(units[0] & 0x03);
    for &u in &units[1..] {
        payload = (payload << 6) | u32::from(u);
    }
    payload
}

/// Combine ADR1 + ADR2 (RCN-217 Table 11) into a DCC locomotive address.
#[must_use]
pub fn decode_address(adr_high: u8, adr_low: u8) -> u16 {
    if adr_high & 0xC0 == 0x80 {
        (u16::from(adr_high & 0x3F) << 8) | u16::from(adr_low)
    } else {
        u16::from(adr_low & 0x7F)
    }
}

/// Pack `id` + payload into `n` 6-bit units (inverse of [`payload_bits`]).
#[cfg(test)]
fn pack_units(id: u8, payload: u32, n: u8) -> [u8; 6] {
    let data_bits = 6 * u32::from(n) - 4;
    let first_extra = 6 * u32::from(n) - 6;
    let top = ((u64::from(payload) >> first_extra) & 0x03) as u8;
    let mut units = [0u8; 6];
    units[0] = (id << 2) | top;
    let mask = if data_bits >= 32 {
        u64::from(u32::MAX)
    } else {
        (1u64 << data_bits) - 1
    };
    let mut rest = u64::from(payload) & mask;
    let rest_bits = data_bits.saturating_sub(2);
    if rest_bits < 64 {
        rest &= (1u64 << rest_bits) - 1;
    }
    for i in (1..n as usize).rev() {
        units[i] = (rest & 0x3F) as u8;
        rest >>= 6;
    }
    units
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::four_of_eight::encode_payload;
    use crate::{Channel, DecoderKind, Error, Info1, Message, Update};

    fn feed_packed(p: &mut Parser, ch: Channel, id: u8, payload: u32, n: u8) -> Vec<Message> {
        let packed = pack_units(id, payload, n);
        feed_units(p, ch, &packed[..n as usize])
    }

    fn feed_units(p: &mut Parser, ch: Channel, units: &[u8]) -> Vec<Message> {
        let mut out = Vec::new();
        for &u in units {
            let code = encode_payload(u).unwrap();
            if let Some(m) = p
                .ingest(Update::Encoded {
                    channel: ch,
                    byte: code,
                })
                .unwrap()
            {
                out.push(m);
            }
        }
        out
    }

    #[test]
    fn pack_roundtrip_dyn() {
        let value = 80u8;
        let sub = 0u8;
        let payload = (u32::from(value) << 6) | u32::from(sub);
        let units = pack_units(7, payload, 3);
        assert_eq!(units[0] >> 2, 7);
        assert_eq!(payload_bits(&units[..3]), payload);
    }

    #[test]
    fn short_address_from_id1_id2() {
        let mut p = Parser::new();
        let msgs = feed_packed(&mut p, Channel::One, 1, 0x00, 2);
        assert_eq!(msgs, vec![Message::AdrHigh(0)]);
        p.begin_cutout();
        let msgs = feed_packed(&mut p, Channel::One, 2, 13, 2);
        assert_eq!(msgs, vec![Message::AdrLow(13)]);
        assert_eq!(p.snapshot().address, Some(13));
    }

    #[test]
    fn long_address() {
        let mut p = Parser::new();
        // ADR1 = 0x80 | (3) → high bits of address 0x0300 + 0x21 = 801
        let high = 0x80 | 0x03;
        let low = 0x21;
        feed_packed(&mut p, Channel::One, 1, u32::from(high), 2);
        p.begin_cutout();
        feed_packed(&mut p, Channel::One, 2, u32::from(low), 2);
        assert_eq!(p.snapshot().address, Some(0x0321));
        assert_eq!(decode_address(high, low), 0x0321);
    }

    #[test]
    fn consist_address_uses_low_7_bits() {
        assert_eq!(decode_address(0x60, 0x85), 5);
    }

    #[test]
    fn id1_does_not_pair_with_stale_id2() {
        let mut p = Parser::new();
        feed_packed(&mut p, Channel::One, 1, 0x00, 2);
        p.begin_cutout();
        feed_packed(&mut p, Channel::One, 2, 13, 2);
        assert_eq!(p.snapshot().address, Some(13));
        p.begin_cutout();
        feed_packed(&mut p, Channel::One, 1, u32::from(0x80u8 | 0x03), 2);
        assert_eq!(p.snapshot().address, Some(13));
        p.begin_cutout();
        feed_packed(&mut p, Channel::One, 2, 0x21, 2);
        assert_eq!(p.snapshot().address, Some(0x0321));
    }

    #[test]
    fn transport_address_does_not_mix_with_encoded_id2() {
        let mut p = Parser::new();
        feed_packed(&mut p, Channel::One, 1, 0x00, 2);
        p.begin_cutout();
        p.ingest(Update::Address(99)).unwrap();
        feed_packed(&mut p, Channel::One, 2, 13, 2);
        assert_eq!(p.snapshot().address, Some(99));
    }

    #[test]
    fn dyn_speed_and_qos_two_datagrams_on_ch2() {
        let mut p = Parser::new();
        p.begin_cutout();
        let speed_payload = u32::from(80u8) << 6;
        let qos_payload = (u32::from(12u8) << 6) | 7;
        let msgs = feed_packed(&mut p, Channel::Two, 7, speed_payload, 3);
        assert_eq!(
            msgs,
            vec![Message::Dyn {
                value: 80,
                subindex: 0
            }]
        );
        assert_eq!(p.snapshot().speed_kmh, Some(80));
        let msgs = feed_packed(&mut p, Channel::Two, 7, qos_payload, 3);
        assert_eq!(
            msgs,
            vec![Message::Dyn {
                value: 12,
                subindex: 7
            }]
        );
        assert_eq!(p.snapshot().qos_percent, Some(12));
    }

    #[test]
    fn dyn_speed_part2_adds_256() {
        let mut p = Parser::new();
        p.ingest(Update::Dyn {
            subindex: 1,
            value: 10,
        })
        .unwrap();
        assert_eq!(p.snapshot().speed_kmh, Some(266));
    }

    #[test]
    fn temperature_and_voltage() {
        let mut p = Parser::new();
        p.ingest(Update::Dyn {
            subindex: 26,
            value: 50,
        })
        .unwrap();
        p.ingest(Update::Dyn {
            subindex: 46,
            value: 10,
        })
        .unwrap();
        let s = p.snapshot();
        assert_eq!(s.temperature_c, Some(0));
        assert_eq!(s.track_voltage_mv, Some(6000));
    }

    #[test]
    fn tanks() {
        let mut p = Parser::new();
        p.ingest(Update::Dyn {
            subindex: 8,
            value: 40,
        })
        .unwrap();
        p.ingest(Update::Dyn {
            subindex: 19,
            value: 99,
        })
        .unwrap();
        let s = p.snapshot();
        assert_eq!(s.tanks[0], Some(40));
        assert_eq!(s.tanks[11], Some(99));
    }

    #[test]
    fn dv2_splits_load_and_speed_128() {
        let mut p = Parser::new();
        p.ingest(Update::Dyn {
            subindex: 2,
            value: 0x05,
        })
        .unwrap();
        assert_eq!(p.snapshot().load, Some(5));
        assert_eq!(p.snapshot().speed_128, None);
        p.ingest(Update::Dyn {
            subindex: 2,
            value: 0x85,
        })
        .unwrap();
        assert_eq!(p.snapshot().speed_128, Some(5));
    }

    #[test]
    fn dv20_assembles_location() {
        let mut p = Parser::new();
        p.ingest(Update::Dyn {
            subindex: 20,
            value: 0x21,
        })
        .unwrap();
        assert_eq!(p.snapshot().location_address, Some(0x21));
        p.ingest(Update::Dyn {
            subindex: 20,
            value: 0x02,
        })
        .unwrap();
        assert_eq!(p.snapshot().location_address, Some(0x0221));
    }

    #[test]
    fn info1_on_channel1() {
        let mut p = Parser::new();
        let msgs = feed_packed(&mut p, Channel::One, 3, 0b0001_0101, 2);
        assert_eq!(
            msgs,
            vec![Message::Info1(Info1 {
                orientation_positive: true,
                travel_negative: false,
                moving: true,
                consist: false,
                request_channel2: true,
            })]
        );
        let i = p.snapshot().info1.expect("info1");
        assert!(i.orientation_positive);
        assert!(i.moving);
        assert!(i.request_channel2);
        assert!(!i.travel_negative);
        assert!(!i.consist);
    }

    #[test]
    fn address_update_from_transport() {
        let mut p = Parser::new();
        assert_eq!(p.ingest(Update::Address(13)).unwrap(), None);
        assert_eq!(p.snapshot().address, Some(13));
    }

    #[test]
    fn ack_nack_encoded() {
        let mut p = Parser::new();
        assert_eq!(
            p.ingest(Update::Encoded {
                channel: Channel::Two,
                byte: 0x0F,
            })
            .unwrap(),
            Some(Message::Ack)
        );
        assert_eq!(
            p.ingest(Update::Encoded {
                channel: Channel::Two,
                byte: 0x3C,
            })
            .unwrap(),
            None
        );
    }

    #[test]
    fn ack_ignores_rest_of_channel_until_cutout() {
        let mut p = Parser::new();
        assert_eq!(
            p.ingest(Update::Encoded {
                channel: Channel::Two,
                byte: 0x0F,
            })
            .unwrap(),
            Some(Message::Ack)
        );
        let qos_payload = (u32::from(12u8) << 6) | 7;
        let msgs = feed_packed(&mut p, Channel::Two, 7, qos_payload, 3);
        assert!(msgs.is_empty());
        assert_eq!(p.snapshot().qos_percent, None);
        p.begin_cutout();
        let msgs = feed_packed(&mut p, Channel::Two, 7, qos_payload, 3);
        assert_eq!(
            msgs,
            vec![Message::Dyn {
                value: 12,
                subindex: 7
            }]
        );
        assert_eq!(p.snapshot().qos_percent, Some(12));
    }

    #[test]
    fn zeit_id14_skipped_then_dyn_parses() {
        let mut p = Parser::new();
        let msgs = feed_packed(&mut p, Channel::Two, 14, 5, 2);
        assert!(msgs.is_empty());
        let qos_payload = (u32::from(12u8) << 6) | 7;
        let msgs = feed_packed(&mut p, Channel::Two, 7, qos_payload, 3);
        assert_eq!(
            msgs,
            vec![Message::Dyn {
                value: 12,
                subindex: 7
            }]
        );
        assert_eq!(p.snapshot().qos_percent, Some(12));
    }

    #[test]
    fn xpom_id8_consumed_as_six_units() {
        let mut p = Parser::new();
        let msgs = feed_packed(&mut p, Channel::Two, 8, 0, 6);
        assert!(msgs.is_empty());
        p.begin_cutout();
        let qos_payload = (u32::from(9u8) << 6) | 7;
        let msgs = feed_packed(&mut p, Channel::Two, 7, qos_payload, 3);
        assert_eq!(
            msgs,
            vec![Message::Dyn {
                value: 9,
                subindex: 7
            }]
        );
    }

    #[test]
    fn unknown_ch1_id_skipped_as_12_bit() {
        let mut p = Parser::new();
        let unit = 15 << 2;
        let code = encode_payload(unit).unwrap();
        assert_eq!(
            p.ingest(Update::Encoded {
                channel: Channel::One,
                byte: code,
            })
            .unwrap(),
            None
        );
        let code2 = encode_payload(0).unwrap();
        assert_eq!(
            p.ingest(Update::Encoded {
                channel: Channel::One,
                byte: code2,
            })
            .unwrap(),
            None
        );
        p.begin_cutout();
        let msgs = feed_packed(&mut p, Channel::One, 1, 0x00, 2);
        assert_eq!(msgs, vec![Message::AdrHigh(0)]);
    }

    #[test]
    fn stationary_ch1_is_srq_without_id() {
        let mut p = Parser::stationary();
        assert_eq!(p.kind(), DecoderKind::Stationary);
        // Extended accessory address 13: 12 bits `1 000 0000 1101`.
        let msgs = feed_units(&mut p, Channel::One, &[0b100000, 0b001101]);
        assert_eq!(
            msgs,
            vec![Message::Srq {
                extended: true,
                address: 13
            }]
        );
    }

    #[test]
    fn mobile_does_not_treat_srq_bits_as_info1() {
        let mut p = Parser::new();
        let msgs = feed_units(&mut p, Channel::One, &[0b100000, 0b001101]);
        assert_ne!(
            msgs,
            vec![Message::Srq {
                extended: true,
                address: 13
            }]
        );
    }

    #[test]
    fn encoded_invalid_code() {
        let mut p = Parser::new();
        assert_eq!(
            p.ingest(Update::Encoded {
                channel: Channel::Two,
                byte: 0x00,
            }),
            Err(Error::InvalidCode)
        );
    }
}
