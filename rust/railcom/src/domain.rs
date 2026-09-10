//! RailCom application types (RCN-217 §5.5).

/// Channel-1 ID 3 (`app:info1`, Table 12). Enabled via CV28 bit 3.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct Info1 {
    /// Bit 0: track-signal polarity as per RCN-210 §2.3. `true` = positive.
    pub orientation_positive: bool,
    /// Bit 1: travel polarity (OW). `true` = negative.
    pub travel_negative: bool,
    /// Bit 2: running state. `true` = moving.
    pub moving: bool,
    /// Bit 3: locomotive is part of a consist.
    pub consist: bool,
    /// Bit 4: request to be addressed so a message can be sent on channel 2.
    pub request_channel2: bool,
}

impl Info1 {
    /// Decode the 8 payload bits of a 12-bit ID 3 datagram.
    #[must_use]
    pub fn from_bits(bits: u8) -> Self {
        Self {
            orientation_positive: bits & 0x01 != 0,
            travel_negative: bits & 0x02 != 0,
            moving: bits & 0x04 != 0,
            consist: bits & 0x08 != 0,
            request_channel2: bits & 0x10 != 0,
        }
    }
}

/// Assembled locomotive telemetry. Absent fields are `None`.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct LocoTelemetryData {
    /// DCC locomotive address from ADR ID 1+2 or [`crate::Update::Address`].
    pub address: Option<u16>,
    /// True speed in km/h (DV 0, or 256 + DV 1).
    pub speed_kmh: Option<u16>,
    /// Reception quality 0–100 (DV 7).
    pub qos_percent: Option<u8>,
    /// Load 0–127 (DV 2 with bit 7 clear), SUSI definition.
    pub load: Option<u8>,
    /// Speed normalised to 128 steps (DV 2 with bit 7 set), 0–127.
    pub speed_128: Option<u8>,
    /// Tank 1–12 contents in percent (DV 8–19).
    pub tanks: [Option<u8>; 12],
    /// Location address (DV 20, 11-bit; or EXT ID 3).
    pub location_address: Option<u16>,
    /// Temperature in °C (DV 26; wire 0 = −50 °C).
    pub temperature_c: Option<i16>,
    /// Track voltage in millivolts (DV 46; 5 V + value × 100 mV).
    pub track_voltage_mv: Option<u16>,
    /// Warning / alarm byte (DV 21).
    pub warning: Option<u8>,
    /// Channel-1 Info1 (ID 3), when present.
    pub info1: Option<Info1>,
}
