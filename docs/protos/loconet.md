# LocoNet Protocol Specification

> Technical reference for the **LocoNet®** bus.
> Implementation: [`go/pkgs/commandstation`](../go/pkgs/commandstation).
>
> Sources:
> - **Digitrax LocoNet Personal Use Edition 1.0** (Digitrax Inc., 16 Oct 1997) —
>   [PDF](https://www.digitrax.com/static/apps/cms/media/documents/loconet/loconetpersonaledition.pdf).
>   The normative source for the wire protocol, opcodes, and slot model.
> - **SV Programming Message Formats v13** (Digitrax PE 1.0 extension, 2002–2006) —
>   [PDF](https://embeddedloconet.sourceforge.net/SV_Programming_Messages_v13_PE.pdf).
>   Defines device (System Variable) programming over `OPC_PEER_XFER`.
> - **JMRI `LnConstants` / `LocoNetSlot`** —
>   [LnConstants](https://webserver.jmri.org/JavaDoc/doc/jmri/jmrix/loconet/LnConstants.html),
>   [LocoNetSlot](https://java.jmri.org/JavaDoc/doc/jmri/jmrix/loconet/LocoNetSlot.html).
>   The de-facto reference for **F9–F28** handling, expanded slots (LocoNet 2),
>   and vendor (Intellibox/Uhlenbrock) function opcodes not in PE 1.0.
> - **OpenLCB LocoNet Connections note** —
>   [old.openlcb.org](http://old.openlcb.org/trunk/documents/notes/LocoNetConnections.html).
>   How LocoNet messages map onto OpenLCB events/datagrams.
> - **LoconetOverTcp** protocol —
>   [loconetovertcp.sourceforge.net](https://loconetovertcp.sourceforge.net/Protocol/LoconetOverTcp.html).
>   The ASCII framing BigFred's `loconet_tcp` driver speaks.
> - **mrrwa/LocoNet** (Arduino embedded library) —
>   [github.com/mrrwa/LocoNet](https://github.com/mrrwa/LocoNet).
> - **tanner87661/LocoNetESP32HB** (ESP32 hybrid library) —
>   [github.com/tanner87661/LocoNetESP32HB](https://github.com/tanner87661/LocoNetESP32HB).
>
> Conventions:
> - Bytes are written in hexadecimal (`0x..` or bare `BF`); LocoNet documents
>   classically wrap opcodes in angle brackets, e.g. `<BF>`.
> - Bit fields are numbered **D7** (MSB) … **D0** (LSB).
> - LocoNet trademarks belong to Digitrax Inc.; message formats reproduced here
>   for interoperability are Copyright Digitrax / Uhlenbrock (see source notes).

## [Overview & design philosophy](#1-overview--design-philosophy)

## [Physical layer](#2-physical-layer)

## [Electrical layer](#3-electrical-layer)

## [Network timing & access (CSMA/CD)](#4-network-timing--access-csmacd)

## [Message format](#5-message-format)

## [Checksum](#6-checksum)

## [Opcode summary](#7-opcode-summary)

## [The refresh slot model](#8-the-refresh-slot-model)

## [Slot data block (`OPC_SL_RD_DATA` / `OPC_WR_SL_DATA`)](#9-slot-data-block-opc_sl_rd_data--opc_wr_sl_data)

## [Loco control messages](#10-loco-control-messages)

## [Functions above F8 (F9–F28 and beyond)](#11-functions-above-f8-f9f28-and-beyond)

## [Expanded slots & LocoNet 2](#12-expanded-slots--loconet-2)

## [Address selection & slot lifecycle](#13-address-selection--slot-lifecycle)

## [Dispatching](#14-dispatching)

## [Switch & sensor messages](#15-switch--sensor-messages)

## [Programming track](#16-programming-track)

## [Device programming (SV) over `OPC_PEER_XFER`](#17-device-programming-sv-over-opc_peer_xfer)

## [Fast clock](#18-fast-clock)

## [Peer-to-peer & immediate packets](#19-peer-to-peer--immediate-packets)

## [LoconetOverTcp framing](#20-loconetovertcp-framing)

## [Embedded implementations (mrrwa, ESP32HB)](#21-embedded-implementations-mrrwa-esp32hb)

## [LocoNet ↔ OpenLCB gateways](#22-loconet--openlcb-gateways)

## [BigFred mapping](#23-bigfred-mapping)

## [Appendix A – Opcode quick reference](#appendix-a--opcode-quick-reference)

## [Appendix B – Worked byte examples](#appendix-b--worked-byte-examples)

---

## 1 Overview & design philosophy

LocoNet is a **peer-to-peer**, event-driven distributed network: every device can
monitor all traffic, and there is **no central poller** in normal operation. Access
is arbitrated with **CSMA/CD** (Carrier Sense Multiple Access with Collision
Detection), the same family used by classic Ethernet.

One device — the **MASTER** (command station) — is privileged only in that it:

- maintains the **refresh stack** (slots) for DCC packet generation, and
- actively generates the DCC track signal.

All other transactions (throttle ↔ throttle, sensor reports, computer interfaces)
flow on the same wire **without** involving the master, as long as they obey the
message format and timing. Devices may join or leave a live bus; the protocol is
tolerant of transients and requires **no unique device IDs**.

This decentralisation is why BigFred sees **other throttles' traffic for free**:
the `loconet_serial` / `loconet_tcp` driver observes every speed/direction/function
packet on the shared bus, not only those it authored
(§23, [`16-dcc-bus/09-external-state-observation.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/specs/bigfred/architecture/16-dcc-bus/09-external-state-observation.md)).

---

## 2 Physical layer

| Property | Value |
|----------|-------|
| Connector | **6-pin USOC RJ12** (TELCO) |
| Cable | Unterminated 26 AWG 3-pair / flat 6-conductor, ~120 Ω |
| Topology | Daisy-chain, star, bus, or any mix; tolerant of cabling |
| Max parallel length | ~2,000 ft total, no point-to-point run > 1,000 ft |
| Termination | **Single** current termination, supplied by the master |

### 2.1 RJ12 pinout

| Pin | Signal | Colour (typical) | Notes |
|-----|--------|------------------|-------|
| 1 | **RAIL_SYNC −** | white | Track-level DCC copy; opposite phase to pin 6 |
| 2 | **Signal ground** | | Logic ground |
| 3 | **LOCONET −** | | Open-collector data line |
| 4 | **LOCONET +** | | Same net as pin 3 (single-ended) |
| 5 | **Signal ground** | | Logic ground |
| 6 | **RAIL_SYNC +** | blue | Track-level DCC copy |

> **BigFred relevance:** a throttle-class interface such as the **Uhlenbrock 63120**
> needs only pins **2/5 (ground)** and **3/4 (data)**. **Never** wire RAIL_SYNC
> (pins 1 & 6) into the interface's logic — see
> [`devices/uhlenbrock-63120.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/devices/uhlenbrock-63120.md) and
> [`hardware/02-loconet-electrical.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/specs/hardware/02-loconet-electrical.md).

The two RAIL_SYNC lines carry a low-power copy of the DCC waveform (for boosters);
the two LocoNet data lines are paralleled in the single-ended implementation, making
the cable **polarity-insensitive**.

---

## 3 Electrical layer

LocoNet is a **wired-OR**, multiple-access linear network. Single-ended levels:

| Symbol | Meaning | Level |
|--------|---------|-------|
| **MARK** (1) | Idle / logic high | LOCONET+/− **> +4.0 V** w.r.t. ground |
| **SPACE** (0) | Active / logic low | LOCONET+/− **< +4.0 V** w.r.t. ground |

| Parameter | Value |
|-----------|-------|
| Receiver hysteresis | ~1.0 V centred on +4.0 V |
| Max LOCONET+/− high | +24 V (nominal +12 V) |
| Min receiver input impedance | 47 kΩ (pins 3&4 → 2&5) |
| Transmitter | Open-collector to ground; sink **50 mA** @ ≤1.6 V; withstand 35 V off |
| Pull-up termination | **15 mA** current source from +12 V (master only) |
| RAIL_SYNC draw | ≤ 15 mA when > 7 V; unloaded 12–26 V |

The idle bus sits at MARK and is **RFI-quiet** (no traffic unless a device sends).
Transmission is **half-duplex**; transmitters monitor their own **transmit echo**
for collision detection.

---

## 4 Network timing & access (CSMA/CD)

### 4.1 Byte framing

LocoNet bytes are standard **asynchronous serial**: **1 start bit, 8 data bits,
1 stop bit**, **LSB first**.

| Parameter | Value |
|-----------|-------|
| Bit time | **60.0 µs** (16.66 kBaud ± 1.5%) |
| PC-friendly rate | 16.457 kBaud (divisor 7 on an NS8250-class UART) |
| Byte spacing | Back-to-back allowed (start bit immediately after previous stop bit) |

> A serial-bridge interface (Uhlenbrock 63120, LocoBuffer) handles this 16.66 kBaud
> wire timing internally; the **host** sees a conventional UART rate
> (BigFred: **57600 8N1**, §23).

### 4.2 Carrier detect & collisions

| Event | Timing |
|-------|--------|
| **CD backoff** (after last SPACE) | 20 bit times ≈ **1.2 ms** |
| CD jitter tolerated | up to 180 µs |
| **Master delay** (non-master devices) | + ≥ 6 bit times ≈ **360 µs** before seize |
| **Priority delay** (first attempt) | + up to 20 bit times; decremented by 1 each retry |
| **BREAK** on collision | force SPACE for **15 bit times** |
| Transmit attempts before failure | ≥ **25**, each ≥ 15 ms |

A device seizes the bus only after the CD backoff elapses; the **master** may seize
immediately when CD releases. All transmitters detect collisions via bad transmit
echo and emit a 15-bit BREAK, which makes every receiver reset its message parser.
Malformed or fragmentary messages are silently ignored; receivers resync on the next
**opcode** byte.

### 4.3 Disconnect / reconnect & purge

| Event | Timing |
|-------|--------|
| **Disconnect** detection | LOCONET held SPACE > **100 ms** |
| **Startup backoff** | wait **250 ms** before first access |
| **Slot purge** (DT200 master) | ~**200 s** of slot inactivity → slot forced to COMMON |
| Recommended **ping** | refresh a slot every ~**100 s** (re-send current speed) |

> **BigFred relevance:** to avoid a slot being purged mid-session the driver must
> re-touch active slots periodically. The `<81>` (`OPC_BUSY`) **"time burner" NOP**
> sent by some masters should simply be stripped and ignored.

### 4.4 PC fast access

A PC interface may infer that the bus is free when the last decoded message implies
no follow-on response, and seize **before** the CD backoff elapses, pre-empting other
devices. Multiple PCs share access by subdividing the 20-bit CD backoff into priority
windows and checking transmit echo + carrier detect.

---

## 5 Message format

Every LocoNet message is **multi-byte**:

```
<OPCODE> <ARG…> <CHECKSUM>
```

- The **opcode** is the **first** byte and the **only** byte with **D7 = 1**.
- All argument bytes and the checksum have **D7 = 0** (7-bit payload each).
- Opcode bits **D6, D5** encode the message length; **D3** flags a follow-on reply:

| D7 | D6 | D5 | D4 | D3 | … | Length |
|----|----|----|----|----|----|--------|
| 1 | 0 | 0 | F | D3 | CBA | **2 bytes** (incl. checksum) |
| 1 | 0 | 1 | F | D3 | CBA | **4 bytes** |
| 1 | 1 | 0 | F | D3 | CBA | **6 bytes** |
| 1 | 1 | 1 | F | D3 | CBA | **variable**: next byte is a 7-bit **byte count** (total length) |

`D3 = 1` implies a follow-on message/reply is expected. The `A,B,C,D,F` bits encode
up to 32 opcodes per length class.

> **BigFred parser:** [`loconet_proto.go`](../go/pkgs/commandstation/loconet_proto.go)
> `lnMsgLen()` decodes `(opcode >> 5) & 0x03` to `{2,4,6,variable}`, and
> `lnStreamParser.PushByte()` reconstructs frames from the serial byte stream,
> resyncing on the next byte with `D7 = 1`.

---

## 6 Checksum

The checksum is the **1's complement of the byte-wise XOR** of all message bytes
**except** the checksum itself.

**Validation:** XOR **all** bytes *including* the checksum — a correct message yields
**`0xFF`**.

```text
chk      = 0xFF XOR (b0 XOR b1 XOR … XOR b[n-1])
valid?   : (b0 XOR b1 XOR … XOR b[n-1] XOR chk) == 0xFF
```

BigFred implementation
([`loconet_proto.go`](../go/pkgs/commandstation/loconet_proto.go)):

```go
func lnChecksumOK(pkt []byte) bool {
    var x byte
    for _, b := range pkt { x ^= b }
    return x == 0xFF
}
// lnAppendChecksum appends chk = x ^ 0xFF
```

Example: idle frame `83 7C` → `0x83 ^ 0x7C = 0xFF`. ✓

---

## 7 Opcode summary

From the LocoNet PE 1.0 opcode list (opcodes in *italics* in the source are
informational/non-final). Length is implied by the opcode's D6/D5 bits (§5).

### 7.1 2-byte messages `<OPC> <CKSUM>`

| Opcode | Value | Meaning | Follow-on |
|--------|-------|---------|-----------|
| `OPC_IDLE` | `0x85` | Force IDLE; broadcast emergency STOP | no |
| `OPC_GPON` | `0x83` | Global power ON | no |
| `OPC_GPOFF` | `0x82` | Global power OFF | no |
| `OPC_BUSY` | `0x81` | Master busy / NOP "time-burner" | no |

### 7.2 4-byte messages `<OPC> <ARG1> <ARG2> <CKSUM>`

| Opcode | Value | Meaning | Follow-on |
|--------|-------|---------|-----------|
| `OPC_LOCO_ADR` | `0xBF` | Request loco address → slot | yes → `<E7>` slot read |
| `OPC_SW_ACK` | `0xBD` | Request switch w/ acknowledge (not DT200) | yes → LACK |
| `OPC_SW_STATE` | `0xBC` | Request switch state | yes → LACK |
| `OPC_RQ_SL_DATA` | `0xBB` | Request slot data/status | yes → `<E7>` slot read |
| `OPC_MOVE_SLOTS` | `0xBA` | Move slot SRC→DEST (also dispatch / NULL move) | yes → `<E7>` / LACK |
| `OPC_LINK_SLOTS` | `0xB9` | Link slot (consist) | yes → `<E7>` |
| `OPC_UNLINK_SLOTS` | `0xB8` | Unlink slot (consist) | yes → `<E7>` |
| `OPC_CONSIST_FUNC` | `0xB6` | Set function bits in a consist uplink element | no |
| `OPC_SLOT_STAT1` | `0xB5` | Write slot STAT1 | no |
| `OPC_LONG_ACK` | `0xB4` | Long acknowledge `<B4><LOPC><ACK1>` | no |
| `OPC_INPUT_REP` | `0xB2` | General sensor input report | no |
| `OPC_SW_REP` | `0xB1` | Turnout sensor state report | no |
| `OPC_SW_REQ` | `0xB0` | Request switch function | no |
| `OPC_LOCO_SND` | `0xA2` | Set slot sound functions (F5–F8) | no |
| `OPC_LOCO_DIRF` | `0xA1` | Set slot direction + F0–F4 | no |
| `OPC_LOCO_SPD` | `0xA0` | Set slot speed | no |

### 7.3 Variable-length messages `<OPC> <COUNT> … <CKSUM>`

| Opcode | Value | Meaning | Follow-on |
|--------|-------|---------|-----------|
| `OPC_WR_SL_DATA` | `0xEF` | Write slot data (10 data bytes / 14-byte msg) | yes → LACK |
| `OPC_SL_RD_DATA` | `0xE7` | Slot data return (10 data bytes / 14-byte msg) | no |
| `OPC_PEER_XFER` | `0xE5` | Peer-to-peer transfer (also SV programming, §17) | no |
| `OPC_IMM_PACKET` | `0xED` | Send n-byte DCC packet immediately (F9+, §11.1) | LACK |

> Opcodes `0xB8`–`0xBF` and `0xA8`–`0xAF` are defined to carry **responses**.
> `OPC_LONG_ACK` (`0xB4`) `<LOPC>` is a copy of the opcode being answered (MSB
> stripped); `LOPC = 0` is also a valid "fail" code.

### 7.4 Extended & vendor opcodes (post-PE 1.0)

These are **not** in PE 1.0; they come from later Digitrax masters (LocoNet 2, §12) or
were reverse-engineered from Uhlenbrock/Intellibox hardware (JMRI `LnConstants`). An
observer on a shared bus must length-decode (§5) and tolerate them.

| Opcode | Value | Len | Meaning |
|--------|-------|-----|---------|
| `RE_OPC_IB2_F9_F12` | `0xA3` | 4 | Intellibox-II F9–F12 (§11.3) |
| `OPC_EXP_REQ_SLOT` | `0xBE` | 4 | Request expanded slot (LocoNet 2, §12) |
| `OPC_EXP_…SPECIAL` (IB2) | `0xD4` | 6 | Intellibox F0–F28 special function groups (§11.3) |
| `OPC_EXP_SEND_FUNCTION_OR_SPEED_AND_DIR` | `0xD5` | 6 | Expanded slot speed/dir/function (§11.2) |
| `OPC_EXP_RD_SL_DATA` | `0xE6` | var | Expanded slot read (a.k.a. `OPC_ALM_READ`) |
| `OPC_EXP_WR_SL_DATA` / `OPC_IMM_PACKET_2` | `0xEE` | var | Expanded slot write / 2nd immediate (a.k.a. `OPC_ALM_WRITE`) |

---

## 8 The refresh slot model

The master keeps an array of up to **120 read/write refresh slots**. A slot holds up
to **10 data bytes** describing a locomotive and controls a task in the DCC refresh
stack. The slot number is the usual **2nd byte (1st argument)** of slot-addressed
messages and works like a "file handle".

| Slot range | Use |
|------------|-----|
| `0` | Special (slot 0 read returns master config; used for dispatch get/put) |
| `1…119` | Normal locomotive refresh slots |
| `120…127` (`0x78…0x7F`) | **Reserved** for system/master control |
| `123` (`0x7B`) | **Fast clock** slot |
| `124` (`0x7C`) | **Programming track** slot (special 10-byte format) |

Slot numbers do **not** imply a fixed loco address — the master allocates them via
address-selection (§13). Up-consisted slots use indirection (the speed byte becomes a
pointer to the consist-top slot).

---

## 9 Slot data block (`OPC_SL_RD_DATA` / `OPC_WR_SL_DATA`)

14-byte message carrying 10 slot data bytes, in transmission order:

```
<E7|EF> <0E> <SLOT> <STAT1> <ADR> <SPD> <DIRF> <TRK> <SS2> <ADR2> <SND> <ID1> <ID2> <CHK>
```

BigFred parses this in
[`parseLnSlotData()`](../go/pkgs/commandstation/loconet_proto.go):
`addr = (adrLo & 0x7F) | ((adrHi & 0x7F) << 7)`, reading `Speed=pkt[5]`,
`DirF=pkt[6]`, `Snd=pkt[10]`.

### 9.1 Byte 1 — `STAT1` (slot status)

| Bit | Name | Meaning |
|-----|------|---------|
| D7 | `SL_SPURGE` | Purge-enable / address-select (internal; not on the wire) |
| D6 | `SL_CONUP` | Consist link-up (see encoding below) |
| D5 | `SL_BUSY` | BUSY/ACTIVE encoding (with D4) |
| D4 | `SL_ACTIVE` | BUSY/ACTIVE encoding (with D5) |
| D3 | `SL_CONDN` | Another slot consist-linked **into** this slot |
| D2 | `SL_SPDEX` | Decoder type / speed-step encoding (with D1, D0) |
| D1 | `SL_SPD14` | " |
| D0 | `SL_SPD28` | " |

**BUSY/ACTIVE (D5,D4):**

| D5 D4 | State | Refreshed? |
|-------|-------|-----------|
| `11` | **IN_USE** | yes |
| `10` | **IDLE** | no |
| `01` | **COMMON** | yes |
| `00` | **FREE** | no |

**Consist (D6,D3):** `11` = mid-consist (linked up & down), `10` = consist top,
`01` = consist sub-member, `00` = free (no consist).

**Speed-step / decoder type (D2,D1,D0):**

| Code | Meaning |
|------|---------|
| `011` | 128-step mode packets |
| `010` | 14-step mode |
| `001` | 28-step (trinary packets) |
| `000` | 28-step / 3-byte packet regular |
| `111` | 128-step, allow advanced DCC consisting |
| `100` | 28-step, allow advanced DCC consisting |

### 9.2 Byte 2 — `ADR` (loco address low 7 bits)

Also the ARG2 of `OPC_LOCO_ADR` `<BF>`.

### 9.3 Byte 3 — `SPD` (speed)

| Value | Meaning |
|-------|---------|
| `0x00` | Speed 0, inertial stop |
| `0x01` | Speed 0, **emergency stop** |
| `0x02…0x7F` | Increasing speed (`0x7F` = max) |

### 9.4 Byte 4 — `DIRF` (direction + F0–F4)

| Bit | Name | Meaning |
|-----|------|---------|
| D7 | — | always 0 |
| D6 | `SL_XCNT` | reserved (0) |
| D5 | `SL_DIR` | **1 = FORWARD** |
| D4 | `SL_F0` | F0 / directional lighting |
| D3 | `SL_F4` | F4 |
| D2 | `SL_F3` | F3 |
| D1 | `SL_F2` | F2 |
| D0 | `SL_F1` | F1 |

> **BigFred bit helpers** (`loconet.go` `getFnFromDirf` / `setFnInDirf`):
> F0 = `0x10`, F1 = `0x01`, F2 = `0x02`, F3 = `0x04`, F4 = `0x08`; direction =
> `0x20`.

### 9.5 Byte 5 — `TRK` (global track status)

| Bit | Name | Meaning |
|-----|------|---------|
| D3 | `GTRK_PROG_BUSY` | 1 = programming track busy |
| D2 | `GTRK_MLOK1` | 1 = master implements LocoNet 1.1; 0 = DT200 |
| D1 | `GTRK_IDLE` | 0 = track paused / broadcast e-stop |
| D0 | `GTRK_POWER` | 1 = DCC packets on (global power up) |

### 9.6 Byte 6 — `SS2` (status 2)

| Bit | Meaning |
|-----|---------|
| D3 | 1 = expansion in ID1/2; 0 = encoded alias |
| D2 | 1 = ID1/2 is **not** ID usage |
| D0 | 1 = slot has suppressed advanced consist |

### 9.7 Byte 7 — `ADR2` (loco address high 7 bits)

`0` ⇒ low byte is a **short** 7-bit NMRA address. Non-zero ⇒ **long** 14-bit address
(maps to CV17/CV18). A DT200 master always treats this as 0.

### 9.8 Byte 8 — `SND` (sound / F5–F8)

| Bit | Name | Meaning |
|-----|------|---------|
| D3 | `SL_SND4` | F8 |
| D2 | `SL_SND3` | F7 |
| D1 | `SL_SND2` | F6 |
| D0 | `SL_SND1` | F5 |

> **BigFred bit helpers** (`getFnFromSnd` / `setFnInSnd`): F5 = `0x01`, F6 = `0x02`,
> F7 = `0x04`, F8 = `0x08`.

### 9.9 Bytes 9–10 — `ID1` / `ID2`

Two 7-bit values forming a 14-bit device-usage ID:

| ID1/ID2 | Meaning |
|---------|---------|
| `00/00` | No ID in use |
| `01/00…7F/01` | PC usage (low nibble = PC type #) |
| `00/02…7F/03` | System reserved |
| `00/04…7F/7E` | Normal throttle range |

---

## 10 Loco control messages

Once a slot is allocated (§13), real-time control uses three 4-byte messages keyed by
**slot number**:

| Message | Bytes | Effect |
|---------|-------|--------|
| `OPC_LOCO_SPD` | `A0 <SLOT> <SPD> <CHK>` | Set speed (§9.3 semantics) |
| `OPC_LOCO_DIRF` | `A1 <SLOT> <DIRF> <CHK>` | Set direction + F0–F4 (§9.4) |
| `OPC_LOCO_SND` | `A2 <SLOT> <SND> <CHK>` | Set F5–F8 (§9.8) |

These do **not** elicit a response. Because they are slot-keyed, an observer must map
slot → address (via a prior slot read) to attribute the change — BigFred keeps a
reverse `slotAddr` map for exactly this
([`loconet.go`](../go/pkgs/commandstation/loconet.go) `slotToAddr`).

> **BigFred builders** ([`loconet_proto.go`](../go/pkgs/commandstation/loconet_proto.go)):
> `lnBuildSetSpeed`, `lnBuildSetDirF`, `lnBuildSetSnd` each append the checksum.
> Direction is folded into the DIRF byte (`0x20`), so `SetSpeed` sends **both** an
> `A0` speed and an `A1` DIRF message to preserve function bits.

### 10.1 Speed scaling

LocoNet slot speed is a 7-bit value (`0x00` stop, `0x01` e-stop, `0x02…0x7F`). BigFred
maps user steps (14/28/128) into `2…127` linearly in
[`scaleToLnSpeed()`](../go/pkgs/commandstation/loconet_proto.go); the decoder
type in `STAT1.D2–D0` (§9.1) selects the DCC packet mode the master emits.

---

## 11 Functions above F8 (F9–F28 and beyond)

The 10-byte slot (§9) only has room for **F0–F4** (`DIRF`) and **F5–F8** (`SND`).
**Functions F9 and up are not stored in the slot at all** and therefore cannot be set
with `OPC_LOCO_DIRF` / `OPC_LOCO_SND`. PE 1.0 throttles/PC interfaces drive them by a
different mechanism. Several approaches exist in the field:

| Range | Primary mechanism | Notes |
|-------|-------------------|-------|
| F0–F4 | `OPC_LOCO_DIRF` `0xA1` (slot) | §10 |
| F5–F8 | `OPC_LOCO_SND` `0xA2` (slot) | §10 |
| **F9–F28** | `OPC_IMM_PACKET` `0xED` (DCC packet) | §11.1 — universal, LocoNet 1.1+ |
| F0–F28 | Expanded command `0xD5` | §11.2 — LocoNet 2 (DCS210/240) only |
| F0–F28 | Vendor opcodes `0xA3` / `0xD4` | §11.3 — Intellibox / Uhlenbrock |
| **F29–F68** | `OPC_IMM_PACKET` `0xED` (RCN-212 groups) | §11.4 — decoder-dependent |
| F29–F32767 | DCC Binary State via `OPC_IMM_PACKET` | §11.4 — NMRA S-9.2.1 |

> Because F9–F28 live outside the slot, a client must remember their state itself.
> JMRI keeps a "virtual extended slot" (`LocoNetSlot.localF9 … localF28`) precisely
> because the command station gives it no place to store them
> ([LocoNetSlot](https://java.jmri.org/JavaDoc/doc/jmri/jmrix/loconet/LocoNetSlot.html)).
>
> **BigFred** does exactly this: the LocoNet driver keeps a per-loco `extFnByA`
> bitmask (`loconet.go`) updated both when it sends F9–F28 and when it observes such
> a packet on the shared bus, so a group send preserves the other functions and
> `ListFunctions` can report them.

### 11.1 F9–F28 via `OPC_IMM_PACKET` (`0xED`)

This is the portable method (works on any master that implements LocoNet 1.1 / the
immediate-packet buffer). The throttle composes a raw **NMRA DCC function packet** and
asks the master to put it on the track via `OPC_IMM_PACKET` (frame layout in §19.2).
LocoNet practice is to send a **whole function group** (a bitmask of all functions in
the group) and to **repeat the packet on the track ~4×**.

**DCC function-group instruction bytes** (NMRA S-9.2.1 / RCN-212), placed *after* the
1- or 2-byte loco address:

| Functions | DCC bytes (after address) | Bit layout of the mask |
|-----------|---------------------------|------------------------|
| F0–F4 | `100 F0 F4 F3 F2 F1` → `0x80 \| bits` | F0=`0x10`, F4=`0x08`, F3=`0x04`, F2=`0x02`, F1=`0x01` |
| F5–F8 | `1011 F8 F7 F6 F5` → `0xB0 \| mask` | F5=`0x01` … F8=`0x08` |
| **F9–F12** | `1010 F12 F11 F10 F9` → `0xA0 \| mask` | F9=`0x01`, F10=`0x02`, F11=`0x04`, F12=`0x08` |
| **F13–F20** | `0xDE` `<mask>` | F13=`0x01` (bit0) … F20=`0x80` (bit7) |
| **F21–F28** | `0xDF` `<mask>` | F21=`0x01` (bit0) … F28=`0x80` (bit7) |
| **F29–F36** | `0xD8` `<mask>` | F29=`0x01`, F30=`0x02`, F31=`0x04`, F32=`0x08` … F36=`0x80` |
| F37–F44 | `0xD9` `<mask>` | F37=bit0 … F44=bit7 |
| F45–F52 | `0xDA` `<mask>` | F45=bit0 … F52=bit7 |
| F53–F60 | `0xDB` `<mask>` | F53=bit0 … F60=bit7 |
| F61–F68 | `0xDC` `<mask>` | F61=bit0 … F68=bit7 |

**Address bytes** (precede the instruction byte(s)):

- **Short** (1–127): one byte `<ADR>`.
- **Long** (128–10239): two bytes `<0xC0 | (ADR>>8)> <ADR & 0xFF>`.

The DCC packet's trailing XOR error byte is **not** carried in the LocoNet message —
the master regenerates it. LocoNet can only carry DCC packets up to **5** payload
bytes (`IM1…IM5`), which covers all standard function groups.

> **NMRA-packet builders (reference, JMRI):** `function9Through12Packet` emits
> `[ADR, 0xA0|mask]`; `function13Through20Packet` emits `[…, 0xDE, mask]`;
> `function21Through28Packet` → `0xDF`; `function29Through36Packet` → `0xD8`
> ([source](https://github.com/JMRI/JMRI/blob/master/java/src/jmri/NmraPacket.java)).

### 11.2 F0–F28 via the expanded command `0xD5` (LocoNet 2)

Newer Digitrax masters (DCS210, DCS240 — "LocoNet 2", §12) accept a compact 6-byte
**slot-addressed** function/speed command,
`OPC_EXP_SEND_FUNCTION_OR_SPEED_AND_DIR` (`0xD5`):

```
D5 <SUB|SLOTHI> <SLOTLO> <THROTTLE_ID> <DATA> <CHK>
  SUB|SLOTHI : (slot >> 7) | subcode      (slot high bits + group selector)
  SLOTLO     : slot & 0x7F
  THROTTLE_ID: slot.id() & 0x7F
  DATA       : function bitmask (or speed for the speed/dir sub-codes)
```

| Sub-code | Value | DATA bit layout |
|----------|-------|-----------------|
| Speed & dir FWD / REV | `0x00` / `0x08` | DATA = 7-bit speed |
| F0–F6 | `0x10` | F1=b0, F2=b1, F3=b2, F4=b3, F0=b4 (`DIRF`-style), F5=b5, F6=b6 |
| F7–F13 | `0x18` | F7=b0, F8=b1, F9=b2, F10=b3, F11=b4, F12=b5, F13=b6 |
| F14–F20 | `0x20` | F14=b0 … F20=b6 |
| F21–F28 (F28 off) | `0x28` | F21=b0 … F27=b6 |
| F21–F28 (F28 on) | `0x30` | F21=b0 … F27=b6 (the sub-code itself encodes F28) |

This addresses the loco by **slot**, so no DCC-packet wrapping is needed, and it
covers **F9–F28 on LocoNet 2 masters only**. F29+ still uses §11.4.

### 11.3 Vendor (Intellibox / Uhlenbrock) function opcodes

Uhlenbrock command stations (and traffic seen from IB-I / IB-II) carry extra functions
on **reverse-engineered** opcodes that are **not** in PE 1.0:

| Opcode | Used by | Functions |
|--------|---------|-----------|
| `0xA3` (`RE_OPC_IB2_F9_F12`) | Intellibox-II | F9–F12: `A3 <slot> <mask> <CHK>`, F9=`0x01` … F12=`0x08` |
| `0xD4` (`RE_OPC_IB2_SPECIAL`) | IB-I v2.x (F0–F28), IB-II (F13–F28) | 6-byte `D4 <slot> <token> <mask> <CHK>` with per-range tokens |

`0xD4` tokens (from JMRI `LnConstants`): `0x08` = F13–F19, `0x05`/`0x06`/`0x07` =
IB-I special F0–F4 / F5–F11 ranges. These appear on the bus from Uhlenbrock hardware
even though BigFred does not generate them; an observer should tolerate/skip them.

> **Z21 relevance:** the Z21's virtual LocoNet stack forwards loco-specific traffic
> including `OPC_LOCO_F912` (the F9–F12 message) and `OPC_EXP_CMD` when the
> `0x02000000` broadcast flag is set — see [`z21.md`](./z21.md) §2.16 and §9.3.1.

### 11.4 F29 and above

- **F29–F68** use the `0xD8`–`0xDC` DCC group bytes (§11.1 table) wrapped in
  `OPC_IMM_PACKET`. These come from RCN-212, not PE 1.0, and **not every decoder
  implements them**.
- **F29–F32767** ("binary states") use the NMRA **DCC Binary State Control
  Instruction** (S-9.2.1), again wrapped in `OPC_IMM_PACKET`. The Z21 documents this
  exact LocoNet path (`LAN_LOCONET_FROM_LAN` + `OPC_IMM_PACKET`); from Z21 FW 1.42 the
  dedicated `LAN_X_SET_LOCO_BINARY_STATE` is preferred — see [`z21.md`](./z21.md)
  §9.3.1.

### 11.5 Observing F9–F28 from the bus

A receiver recognises an extended-function command by decoding the embedded DCC packet
from an `OPC_IMM_PACKET` and checking the instruction byte: mask `… == 0xA0` ⇒ F9–F12,
`… == 0xDE00` ⇒ F13–F20, etc. (JMRI `SlotManager.isExtFunctionMessage`). The loco
**address** is taken from the embedded DCC address bytes — *not* from a slot — so an
observer needs no prior slot read to attribute the change.

---

## 12 Expanded slots & LocoNet 2

Original LocoNet (PE 1.0, "LocoNet 1.1") has **120** usable refresh slots and the
F0–F8 slot model above. Later Digitrax masters (**DCS210**, **DCS240**) added a second
protocol level — informally **"LocoNet 2"** — with **expanded slots** (hundreds of
slots, ~`0x000…0x77F`) and the compact `0xD5` function/speed command (§11.2).

| Opcode | Value | Meaning |
|--------|-------|---------|
| `OPC_EXP_REQ_SLOT` | `0xBE` | Request an expanded slot for an address |
| `OPC_EXP_RD_SL_DATA` | `0xE6` | Expanded slot data read (also seen as `OPC_ALM_READ`) |
| `OPC_EXP_WR_SL_DATA` | `0xEE` | Expanded slot data write (also `OPC_IMM_PACKET_2` / `OPC_ALM_WRITE`) |
| `OPC_EXP_SEND_…` | `0xD5` | Expanded speed/dir/function command (§11.2) |

**Protocol detection.** Slot `STAT1.D2` (`GTRK_MLOK1`, §9.5) tells you the master
implements LocoNet 1.1 (0 = DT200). JMRI further distinguishes LocoNet 1 vs 2 with the
`LOCONETPROTOCOL_ONE` / `LOCONETPROTOCOL_TWO` levels, probing whether `0xBE`/`0xE6`
expanded-slot traffic is honoured.

> **BigFred scope:** BigFred targets the **LocoNet 1.1** surface (120 slots, F0–F8).
> Expanded slots and the `0xD5` command are **not** generated; the parser tolerates
> them on a shared bus by length-decoding (§5) and ignoring unknown opcodes.

---

## 13 Address selection & slot lifecycle

```
Throttle ──► OPC_LOCO_ADR  BF 00 <ADR> <CHK>     (request address)
Master   ──► OPC_SL_RD_DATA E7 0E … <CHK>          (slot containing that address)
                                                    │
              ┌─────────────────────────────────────┘
              ▼
Throttle ──► OPC_MOVE_SLOTS BA <slot> <slot> <CHK> (NULL move → mark IN_USE)
Master   ──► OPC_SL_RD_DATA E7 …                    (confirm slot now IN_USE)
```

1. **Request address** with `OPC_LOCO_ADR` `<BF> <0> <ADR> <CHK>`.
   - Short 7-bit address: high byte = 0; analog/zero-stretch: both bytes 0;
     long 14-bit: high byte non-zero (most-significant bits).
2. Master replies with `OPC_SL_RD_DATA` `<E7>` for the slot containing the address.
   If the address is new, the master loads it into a **FREE** slot (speed 0, forward,
   functions off, 128-step) and returns that slot. If **no free slot**, it returns
   `OPC_LONG_ACK` `<B4> <3F> <00>` (fail code 0).
3. If the returned `STAT1` shows **COMMON / IDLE / NEW**, the throttle promotes it to
   **IN_USE** via a **NULL MOVE** (`OPC_MOVE_SLOTS` with `SRC == DEST`).
4. If the loco is already **IN_USE** or **up-consisted**, do **not** use it (unlink
   first if consisted).

After this the throttle owns the slot and updates Speed/Dir/Functions (§10). On
reconnect, a throttle re-reads the slot and continues only if the state matches its
remembered value; otherwise it re-runs the logon.

> **BigFred:** `ensureSlotLocked()` issues `OPC_LOCO_ADR`, waits for the matching
> `E7` slot read, and caches `addr↔slot`. `querySlotLocked()` issues `OPC_RQ_SL_DATA`
> `<BB> <slot> <0>` to refresh current state before toggling a single function bit.
> BigFred currently relies on the master's allocation and does **not** issue the NULL
> MOVE itself; it caches slot state and serialises request/response sequences under a
> mutex (`reqMu`) with a sync channel.

---

## 14 Dispatching

A "one-deep" mechanism to hand a prepared slot to a simple throttle:

| Action | Message | Result |
|--------|---------|--------|
| **DISPATCH PUT** | `OPC_MOVE_SLOTS BA <SRC> <00> <CHK>` (DEST = 0) | Source slot marked as the dispatch slot |
| **DISPATCH GET** | `OPC_MOVE_SLOTS BA <00> <xx> <CHK>` (SRC = 0) | `E7` slot read of the dispatch slot, or `B4 3A 00` (fail) if none |

It is illegal to move to/from slots 120–127.

---

## 15 Switch & sensor messages

LocoNet accessory/feedback uses DS54-style 11-bit addressing (`A0…A10`), where the two
LS bits of `SW1` select one of four output/input pairs.

### 15.1 `OPC_SW_REQ` `0xB0` — request switch (turnout)

```
B0 <SW1> <SW2> <CHK>
SW1 = 0 A6 A5 A4 - A3 A2 A1 A0     (7 LS address bits)
SW2 = 0 0 DIR ON - A10 A9 A8 A7    (control + 4 MS address bits)
  DIR = 1 Closed/GREEN, 0 Thrown/RED
  ON  = 1 output ON, 0 output OFF
```

`OPC_SW_ACK` `0xBD` is the acknowledged variant (returns LACK; not on DT200).
`OPC_SW_STATE` `0xBC` requests current state.

### 15.2 `OPC_INPUT_REP` `0xB2` — general sensor input

```
B2 <IN1> <IN2> <CHK>
IN1 = 0 A6 A5 A4 - A3 A2 A1 A0
IN2 = 0 X I L - A10 A9 A8 A7
  I = 0 DS54 "aux" inputs, 1 "switch" inputs (4K sensor space)
  L = 0 input LOW (0 V), 1 input HIGH (≥ +6 V)
  X = 1 (control bit; 0 reserved)
```

### 15.3 `OPC_SW_REP` `0xB1` — turnout sensor / output report

Two encodings selected by the second control bit: input levels (`I`,`L`) for turnout
feedback, or current output levels (`C` = closed line on, `T` = thrown line on).

> **BigFred scope:** the current driver focuses on **mobile decoder** control
> (speed/dir/F0–F8) and observation. Accessory switching and sensor decoding are
> **not** implemented in `loconet.go`; on the Z21 path accessory/feedback ride other
> messages (see [`z21.md`](./z21.md) §5, §7).

---

## 16 Programming track

The programming track is **special slot 124 (`0x7C`)**, a shared asynchronous
resource. Writing to it starts a task; an immediate **LACK** indicates acceptance,
and an `OPC_SL_RD_DATA` `<E7>` from slot 124 carries the final result.

### 16.1 Task start

```
EF 0E 7C <PCMD> <00> <HOPSA> <LOPSA> <TRK> <CVH> <CVL> <DATA7> <00> <00> <CHK>
```

Immediate LACK codes:

| LACK | Meaning |
|------|---------|
| `B4 7F 7F` | Function not implemented, no reply |
| `B4 7F 00` | Programmer busy, task aborted, no reply |
| `B4 7F 01` | Accepted; `<E7>` reply at completion |
| `B4 7F 40` | Accepted **blind**; no `<E7>` reply |

### 16.2 `PCMD` — programmer command byte

| Bit | Meaning |
|-----|---------|
| D6 | Write/Read: 1 = Write, 0 = Read |
| D5 | Byte mode: 1 = byte op, 0 = bit op |
| D4 | TY1 (programming type select) |
| D3 | TY0 (programming type select) |
| D2 | Ops mode: 1 = ops mode on mainline, 0 = service mode on programming track |

**Type codes:**

| Byte | Ops | TY1 | TY0 | Meaning |
|------|-----|-----|-----|---------|
| 1 | 0 | 0 | 0 | Paged byte R/W (service track) |
| 1 | 0 | 0 | 1 | Direct byte R/W (service track) |
| 0 | 0 | 0 | 1 | Direct bit R/W (service track) |
| x | 0 | 1 | 0 | Physical register byte R/W (service track) |
| 1 | 1 | 0 | 0 | Ops-mode byte program, no feedback |
| 1 | 1 | 0 | 1 | Ops-mode byte program, feedback |
| 0 | 1 | 0 | 0 | Ops-mode bit program, no feedback |
| 0 | 1 | 0 | 1 | Ops-mode bit program, feedback |

### 16.3 CV addressing & data

```
CVH = 0 0 CV9 CV8 - 0 0 D7 CV7    (high 3 bits of CV# + MS data bit)
CVL = 0 CV6 CV5 CV4 - CV3 CV2 CV1 CV0
DATA7 = 0 D6 D5 D4 - D3 D2 D1 D0   (MS data bit lives in CVH.D1)
```

### 16.4 Final reply `PSTAT`

```
E7 0E 7C <PCMD> <PSTAT> <HOPSA> <LOPSA> <TRK> <CVH> <CVL> <DATA7> <00> <00> <CHK>
```

| Bit | Meaning |
|-----|---------|
| D3 | User aborted |
| D2 | Failed to detect read-compare acknowledge |
| D1 | No write acknowledge from decoder |
| D0 | Service-mode track empty (no decoder) |

> **BigFred:** `ReadCV` / `WriteCV` are implemented for the **programming track**
> (`ProgrammingTrackMode`) using service-mode **direct byte** access
> ([`loconet.go`](../go/pkgs/commandstation/loconet.go) `readCVLocked` /
> `writeCVLocked`; builders `lnBuildProgTask` / `parseLnProgReply` with `PCMD` `0x2B`
> read, `0x6B` write — the values observed from real command stations). The driver
> sends the `0xEF` slot-`0x7C` task, then resolves the **LACK** and the final `0xE7`
> reply (PSTAT + value). **POM** (main-track) CV access is rejected because it needs
> RailCom; for that, use the **Z21** path ([`z21.md`](./z21.md) §6).

---

## 17 Device programming (SV) over `OPC_PEER_XFER`

Beyond programming **decoders** on the programming track (§16), LocoNet programs the
**configuration of LocoNet devices themselves** (feedback modules, signal drivers,
the Uhlenbrock 63120, etc.) through **System Variables (SVs)**. This is a PE 1.0
extension carried in the 16-byte `OPC_PEER_XFER` (`0xE5` `0x10`) message
([SV Programming v13](https://embeddedloconet.sourceforge.net/SV_Programming_Messages_v13_PE.pdf)).

An `0xE5` message is recognised as SV programming by its **4th byte** and the **upper
nibbles** of the 6th and 11th bytes. Two layouts exist; **format 2 is recommended**
for new designs:

```
E5 10 <SRC> <SV_CMD> <SV_TYPE> <SVX1> <DST_L> <DST_H>
      <SV_ADRL> <SV_ADRH> <SVX2> <D1> <D2> <D3> <D4> <CHK>
```

(Format 1 — legacy — is `E5 10 <SRC> <DST> 01 <PXCT1> D1 D2 D3 D4 <PXCT2> D5 D6 D7 D8 <CHK>`.)

### 17.1 Field usage (format 2)

| Field | Meaning |
|-------|---------|
| `SRC` | 7-bit source address (`0x0–0xF` typically PCs; `0x10–0x7F` other devices) |
| `SV_TYPE` | must be `0x02` for this format |
| `SVX1` | `0 0 0 1 D3 D2 D1 D0` — D7 bits of `SV_ADRH/L`, `DST_H/L` |
| `DST_L/H` | 16-bit device address being programmed (no broadcast in format 2) |
| `SV_ADRL/H` | 16-bit EEPROM/SV address; multi-byte ops use `Addr, Addr+1, …` |
| `SVX2` | `0 0 0 1 D3 D2 D1 D0` — D7 bits of `D1…D4` |
| `D1…D4` | payload; `D1` for single-byte ops, `D1` = LSB for multi-byte |

### 17.2 `SV_CMD` commands

| Cmd | Meaning | Reply (`.6` set) |
|-----|---------|------------------|
| `0x01` | Write 1 byte (from `D1`) | `0x41` |
| `0x02` | Read 1 byte (into `D1`) | `0x42` |
| `0x03` | Masked write 1 byte (`D1` data, `D2` mask) | `0x43` |
| `0x05` | Write 4 bytes (`D1…D4`) | `0x45` |
| `0x06` | Read 4 bytes (`D1…D4`) | `0x46` |
| `0x07` | **Discover** — all devices report identity | `0x47` |
| `0x08` | **Identify** — addressed device reports identity | `0x48` |
| `0x09` | **Change Address** — match identity, set new `DST_L/H` | `0x49` |
| `0x0F` | **Reconfigure** / reset to apply new config | `0x4F` |

Identity replies return `MANUFACTURER_ID` in `SV_ADRL`, `DEVELOPER_ID` in `SV_ADRH`,
`PRODUCT_ID` in `D1/D2`, and the **serial number** in `D3/D4` (each 16-bit value LSB
first). The Discover→Identify→Change-Address sequence resolves devices that ship with
the same default address.

### 17.3 Standard SV locations

| SV | Meaning |
|----|---------|
| `SV 1` | EEPROM size (`0`=256 B, `1`=512 B, `2`=1024 B, `3`=2048 B, `4`=4096 B) |
| `SV 2` | Software version (0–255) |
| `SV 3` / `SV 4` | Serial number low / high (user-configurable if vendor allows) |

`MANUFACTURER_ID` uses the NMRA DCC manufacturer number; DIY developers use NMRA
manufacturer **13** and manage their own `DEVELOPER_ID`s.

> **BigFred scope:** SV programming is **not** implemented in the LocoNet driver.
> It is documented here because the **Uhlenbrock 63120** interface and many LocoNet
> feedback/accessory boards are configured this way (LNCV is a related Uhlenbrock
> variant) — see [`devices/uhlenbrock-63120.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/devices/uhlenbrock-63120.md).

---

## 18 Fast clock

The system fast clock lives in **slot 123 (`0x7B`)**. Write with `OPC_WR_SL_DATA`,
read/sync via slot read of `0x7B`. Devices keep a local clock and only re-sync on the
SYNC read (~every 70–100 s); they must **not** continuously poll the slot.

```
EF 0E 7B <CLK_RATE> <FRAC_MINSL> <FRAC_MINSH> <256-MINS_60> <TRK>
         <256-HRS_24> <DAYS> <CLK_CNTRL> <ID1> <ID2> <CHK>
```

| Field | Meaning |
|-------|---------|
| `CLK_RATE` | 0 = freeze, 1 = 1:1, 10 = 10:1, … max 0x7F (128:1) |
| `FRAC_MINSL/H` | Sub-minute counter (reset on valid SYNC) |
| `256-MINS_60` | 256 − minutes (mod 0–59) |
| `256-HRS_24` | 256 − hours (mod 0–23) |
| `DAYS` | 24-hour rollovers (positive count) |
| `CLK_CNTRL` | D6 = 1 valid clock info |
| `ID1/ID2` | Device that last set the clock (`00/00` = none; `7F/7x` = PC) |

> **BigFred scope:** fast clock is **not** implemented in the LocoNet driver.

---

## 19 Peer-to-peer & immediate packets

### 19.1 `OPC_PEER_XFER` `0xE5`

Moves 8 data bytes SRC→DST (16-byte message). `DSTL/DSTH = 0` is broadcast; `SRC = 0`
is master; `SRC = 7F` is a throttle message transfer. The `PXCT1`/`PXCT2` bytes carry
the MS bit of each data byte plus address/data type codes (e.g. ANSI text). Used for
throttle text, LNCV-style transfers, and vendor extensions. The SV device-programming
protocol (§17) rides this opcode.

### 19.2 `OPC_IMM_PACKET` `0xED`

Sends an n-byte DCC packet immediately (not entered into the refresh stack). This is
the carrier for **F9–F28+** functions (§11.1) and DCC binary states:

```
ED 0B 7F <REPS> <DHI> <IM1> <IM2> <IM3> <IM4> <IM5> <CHK>
  DHI  = 0 0 1 IM5.7 - IM4.7 IM3.7 IM2.7 IM1.7   (MS bits of IM1..5)
  REPS = 0 <#IM bytes:D6..D4> 0 <repeat count:D2..D0>
```

| Field | Meaning |
|-------|---------|
| `0x7F` | "immediate" sub-type (a DCC packet to the main track) |
| `REPS` | bits 6–4 = number of `IMx` bytes; bits 2–0 = on-track repeat count (1–8) |
| `DHI` | the D7 (MSB) of each `IM1…IM5`, since payload bytes are 7-bit |
| `IM1…IM5` | the DCC packet bytes (address + instruction), **without** the XOR error byte |

LACK `B4 7D 7F` = command OK; `B4 7D 00` = buffer busy. Not implemented on DT200.

The `DHI` byte's top three bits are the fixed pattern `0 0 1` per the Digitrax spec
(so `DHI` is always ≥ `0x20`); bits D4…D0 are the D7 (MSB) of `IM5…IM1` respectively.

> **Note — JMRI deviation:** JMRI's `SlotManager.sendPacket` builds `DHI` from the
> payload MSBs **without** the fixed `0x20` bit. Real command stations accept both;
> this doc follows the normative Digitrax bit layout.

**Example — F9 ON, short address 3** (DCC packet `03 A1`, 2 bytes, 4 repeats):

```
ED 0B 7F 23 20 03 21 00 00 00 00 <CHK>
         │  │  │  └─ IM2 = 0xA1 & 0x7F = 0x21  (0xA0 | F9)
         │  │  └──── IM1 = 0x03         (address 3)
         │  └─────── DHI = 0x20         (fixed 001 high bits; no IMx has D7 set)
         └────────── REPS = 0x23        (#bytes=2 → bits6-4=010, repeats=3+1)
```

(The decoder sees the regenerated full packet `03 A1 A2`.)

> **BigFred:** the LocoNet driver emits `OPC_IMM_PACKET` for **F9–F28**
> (`lnBuildImmPacket` + `dccFnGroupPacket`, repeated `lnImmRepeats`×) and decodes it
> on receive (`decodeImmDccPacket` → `dccPacketFunctions`) to observe external F9–F28
> changes. F29+ and DCC binary states are still out of scope (§23.3).

---

## 20 LoconetOverTcp framing

BigFred's **`loconet_tcp`** kind supports two TCP wire formats. The **default**
(`tcp://`) is **raw binary** LocoNet over TCP (§20.4). The ASCII
[LoconetOverTcp](https://loconetovertcp.sourceforge.net/Protocol/LoconetOverTcp.html)
protocol described in this section — spoken by `LbServer`-style gateways and the Digikeijs
DR5000 LBServer LAN mode — is the
[`lnTCPASCIITransport`](../go/pkgs/commandstation/loconet_tcp_ascii.go) driver, selected with
the `lbserver://` scheme.

### 20.1 Line syntax

- One TCP connection per session; **human-readable ASCII**, line-oriented.
- A line ends with **CR** (`0x0D`) and/or **LF** (`0x0A`); empty lines are discarded.
- Each line starts with an uppercase **token**, optionally followed by a
  space-separated parameter.
- LocoNet bytes are carried as **space-separated hex** (e.g. `83 7C`).

### 20.2 Tokens

| Token | Direction | Since | Meaning |
|-------|-----------|-------|---------|
| `VERSION <info>` | server → client | v0 | Server identification on connect |
| `RECEIVE <hex…>` | server → client | v0 | A LocoNet message received from the bus (distributed to **all** clients) |
| `SEND <hex…>` | client → server | v1 | Request to transmit a LocoNet message to the bus |
| `SENT OK\|ERROR <info>` | server → client | v1 | Response to a `SEND` (sent **after** the `RECEIVE` echo) |
| `TIMESTAMP <µs>` | server → client | v2 | Microseconds since server start; precedes the timestamped token |
| `BREAK [<µs>]` | server → client | v2 | Collision/break detected on the bus |
| `ERROR CHECKSUM <hex…>` | server → client | v2 | Well-formed message, checksum failed |
| `ERROR LINE <info>` | server → client | v2 | Sub-byte-layer error (framing/stop bit) |
| `ERROR MESSAGE <hex…>` | server → client | v2 | Incomplete/inconsistent message |

**Version history:** v0 (Jun 2002, receive-only), v1 (Sep 2002, full duplex), v2
(Apr 2015, error reporting + timestamps).

### 20.3 Echo semantics

A `SEND` is echoed back as a `RECEIVE` (the bus echo), and **then** a `SENT` token
confirms success/failure. Clients expecting a command-station reply see it right after
the `SENT` token (skipping any `OPC_BUSY` / `BREAK`). This mirrors the half-duplex
echo model of the raw bus (§4.1).

> **BigFred implementation:** `lnTCPASCIITransport.WritePacket()` emits
> `SEND <hex…>\r\n` (refusing bad checksums); `readLoop()` parses `RECEIVE` lines into
> packets (validating checksum), logs `VERSION` / `SENT`, and ignores other tokens.
> Parsing is shared with the serial path through `lnParseHexBytes()`. The reader is a
> plain blocking line reader (it never arms a per-read deadline that could drop a
> half-received `RECEIVE` line), it locates the `RECEIVE` token anywhere in the line
> (tolerating a leading `TIMESTAMP`), and it trims the payload to the opcode's length
> code before checksumming — matching RocRail's `lbserver.c` behaviour.

### 20.4 Raw binary variant (no ASCII framing)

Not every LocoNet-over-TCP peer speaks the ASCII LbServer protocol: some bridges stream
**raw LocoNet bytes** straight over the socket (opcode … checksum inclusive), with no
`SEND`/`RECEIVE` lines. This is the protocol of RocRail's **`lbtcp`** client
(`rocdigs/impl/loconet/lbtcp.c`), as opposed to its ASCII `lbserver.c`. Pointing the
ASCII transport at such a peer connects fine but every request times out, because no
`RECEIVE` line ever arrives.

BigFred handles this with a second transport,
[`lnTCPBinaryTransport`](../go/pkgs/commandstation/loconet_tcp_binary.go)
(`NewLocoNetTCPBinary`): it `Write`s the raw message bytes and reassembles inbound frames
with the same `lnStreamParser` (§5) the serial transport uses. Raw binary is the **default**
and the more common case, so it is selected by the bare `tcp://host:port` scheme; the ASCII
LbServer protocol (`lnTCPASCIITransport`) is selected with `lbserver://host:port`
(see [`05-domain-model/01-entities.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/specs/bigfred/architecture/05-domain-model/01-entities.md)).

---

## 21 Embedded implementations (mrrwa, ESP32HB)

These libraries are **not** part of BigFred but document real-world bit-level behaviour
useful when reasoning about a physical bus or building a custom interface.

### 21.1 mrrwa/LocoNet (Arduino)

[github.com/mrrwa/LocoNet](https://github.com/mrrwa/LocoNet) — the reference embedded
library:

- Interrupt-driven Rx into a **circular FIFO**; `LocoNet.receive()` returns the head
  packet or `NULL`.
- Successfully sent packets are (by default) appended to the receive buffer, so the
  app handles them like any other bus traffic — the **echo** model again.
- Maintains Tx/Rx statistics; **invalid packets are discarded** and an `RxErrors`
  counter is incremented.
- Uses a 16-bit timer + **Input Capture (ICP)** for wire timing (UNO `TIMER1`/`ICP`,
  MEGA `TIMER5`/`ICP5`, STM32 `TIM2`+`EXTI`, ESP8266 `TIMER1`).
- **Signal polarity** is configurable (`LN_SW_UART_RX_INVERTED` /
  `LN_SW_UART_TX_INVERTED`) — most interface circuits invert the LocoNet line.

### 21.2 tanner87661/LocoNetESP32HB (ESP32 hybrid)

[github.com/tanner87661/LocoNetESP32HB](https://github.com/tanner87661/LocoNetESP32HB) —
**hardware UART receiver + timer-interrupt transmitter**. Hardware UART Rx keeps valid
reception even with WiFi active (which jitters timers); Tx errors are easy to detect
and the library auto-resends.

Receive buffer (`lnReceiveBuffer`) carries timing useful for request/response logic:

| Field | Meaning |
|-------|---------|
| `lnMsgSize`, `lnData[]` | Message bytes |
| `errorFlags` | Error/status bits (below) |
| `reqID` | ID of the request that caused this message |
| `echoTime` | µs between request and its echo |
| `reqRespTime` | µs between request and reply |

**Error flags:**

| Flag | Value | Meaning |
|------|-------|---------|
| `errorCollision` | `0x01` | Bus collision |
| `errorFrame` | `0x02` | Framing error |
| `errorTimeout` | `0x04` | Timeout |
| `errorCarrierLoss` | `0x08` | Carrier loss |
| `msgEcho` | `0x10` | Message is the echo of our own send |
| `msgIncomplete` | `0x20` | Incomplete message |
| `msgXORCheck` | `0x40` | Checksum (XOR) failed |
| `msgStrayData` | `0x80` | Stray data |

A valid message must always have a correct **opcode**, data bytes, and **XOR check
byte** before sending (mirrors §5–§6). Inverted logic is typical (`InverseLogic`).

---

## 22 LocoNet ↔ OpenLCB gateways

For interoperability with **OpenLCB / LCC** layouts, LocoNet traffic can be bridged to
OpenLCB events and datagrams
([OpenLCB note](http://old.openlcb.org/trunk/documents/notes/LocoNetConnections.html)).
This is **not** part of BigFred, but it informs how a shared bus is observed and how a
neutral "every device sees every packet" model maps to a publish/subscribe network.

- **LocoNet layout-control messages are already producer/consumer events** — they are
  broadcast to unknown listeners, exactly like OpenLCB P/C events. Mapping each to a
  unique `EventID` makes the bridge straightforward.
- **EventID layout** used by the gateway:

| Byte 0 | Byte 1 | Bytes 2…6 |
|--------|--------|-----------|
| Unique ID | LocoNet ID | LocoNet message content (opcode + args, **minus** check byte) |

- **Transparent (raw) mapping.** Short LocoNet messages (≤ 4 bytes) fit a single
  OpenLCB event; longer ones (slot reads, transponding, LISSY) span multiple event
  messages and are reassembled using the §5 length code. This survives protocol
  additions because the gateway never parses message semantics.
- **LocoNet ID** (a 7-bit value separating co-located radio LocoNets) is carried so
  multiple LocoNets do not collide at large modular meets.
- **Flow control:** OpenLCB is much faster than LocoNet, so the OpenLCB→LocoNet
  direction needs buffering; LocoNet→OpenLCB needs almost none.
- **Synchronisation caveat:** LocoNet has no general response/ack, and programs model
  state by watching the **echoed** stream (§4.1, §20.3). OpenLCB events are not echoed
  the same way, so a bridge must decide whether to re-emit events it places on the bus
  (an "echo" flag is proposed).

> **BigFred relevance:** the same *shared-bus, echo-driven* property that OpenLCB
> gateways exploit is why BigFred observes other throttles' speed/function changes for
> free (§1, §23.3). BigFred does not implement an OpenLCB bridge.

---

## 23 BigFred mapping

How this spec maps to BigFred's drivers
([`pkgs/loco/commandstation/`](../go/pkgs/commandstation/)):

### 23.1 Transports

| Kind | Transport | Wire | URI |
|------|-----------|------|-----|
| `loconet_serial` | `lnSerialTransport` | UART **57600 8N1** to a LocoBuffer-class interface (Uhlenbrock 63120) | `serial:///dev/loconet-63120:57600` |
| `loconet_tcp` | `lnTCPBinaryTransport` | Raw binary LocoNet over TCP (§20.4; RocRail `lbtcp`); **default** | `tcp://<host>:<port>` |
| `loconet_tcp` | `lnTCPASCIITransport` | LoconetOverTcp (§20) ASCII over TCP (LbServer) | `lbserver://<host>:<port>` |

The serial transport reconstructs packets with `lnStreamParser` (§5); the TCP
transport parses `RECEIVE` lines (§20). Both push validated packets onto a shared
`rxCh`, demultiplexed by a single `dispatch()` goroutine.

### 23.2 Opcodes BigFred uses

| Constant | Value | Used for |
|----------|-------|----------|
| `lnOPC_LOCO_ADR` | `0xBF` | Address → slot request (`ensureSlotLocked`) |
| `lnOPC_RQ_SL_DATA` | `0xBB` | Slot status query (`querySlotLocked`) |
| `lnOPC_LOCO_SPD` | `0xA0` | `SetSpeed` |
| `lnOPC_LOCO_DIRF` | `0xA1` | Direction + F0–F4 |
| `lnOPC_LOCO_SND` | `0xA2` | F5–F8 |
| `lnOPC_SL_RD_DATA` | `0xE7` | Slot data parsing / observation |
| `lnOPC_LONG_ACK` | `0xB4` | (recognised) |
| `lnOPC_BUSY/GPOFF/GPON/IDLE` | `0x81/0x82/0x83/0x85` | (recognised) |

### 23.3 Capability & limits

| Capability | Status | Reason |
|------------|--------|--------|
| Speed / direction | ✅ | `OPC_LOCO_SPD` + `OPC_LOCO_DIRF` |
| Functions **F0–F8** | ✅ | DIRF (F0–F4) + SND (F5–F8), slot-keyed |
| Functions **F9–F28** | ✅ | `OPC_IMM_PACKET` DCC groups (§11.1); `sendExtFnLocked` + per-loco `extFnByA` cache |
| Functions **F29+** | ❌ | DCC groups / binary state via `OPC_IMM_PACKET` (§11.4) — out of scope |
| Observe other throttles | ✅ | Shared bus; `observe()` parses `A0/A1/A2/E7` (F0–F8) **and** `ED` (F9–F28) |
| **CV read / write (prog track)** | ✅ | Service-mode direct byte via prog slot `0x7C` (§16); `ReadCV`/`WriteCV` for `prog` mode |
| **CV read / write (POM)** | ❌ | Main-track read needs RailCom; `ReadCV`/`WriteCV` reject `pom` mode |
| Accessory / sensors | ❌ | `B0/B1/B2` not implemented |
| SV device programming | ❌ | `OPC_PEER_XFER` SV (§17) not implemented |
| Expanded slots / LocoNet 2 | ❌ | `0xBE/0xD5/0xE6/0xEE` tolerated, not generated (§12) |
| Fast clock, consist, dispatch | ❌ | Out of current driver scope |

> F9–F28 and service-mode CV access were added to the LocoNet driver. The remaining
> gaps vs **Z21** are F29+, POM CV access (RailCom) and accessory/sensor control, so
> BigFred docs still prefer Z21 for the RailBOX RB1110 and use DR5000 over LocoNet
> when this surface suffices — see
> [`commandstations/dr5000.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/dr5000.md) §8 and
> [`commandstations/rb1110.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/rb1110.md) §8.

### 23.4 Concurrency & slot safety

- `reqMu` serialises request/response sequences (one in flight at a time).
- `beginSync()/endSync()` gate the `syncCh` so unsolicited bus traffic is not consumed
  while no one is waiting; `dispatch()` always feeds `observe()`.
- `slotByAd` / `slotAddr` cache the address↔slot mapping; `dirfByA` / `sndByA` cache
  function bytes so a single function toggle does not clobber the others.
- Checksum is enforced on **send** (`sendLocked`) and **receive** (transport layer).

---

## Appendix A – Opcode quick reference

| Hex | Name | Len | Args | Reply |
|-----|------|-----|------|-------|
| `0x81` | `OPC_BUSY` | 2 | – | – |
| `0x82` | `OPC_GPOFF` | 2 | – | – |
| `0x83` | `OPC_GPON` | 2 | – | – |
| `0x85` | `OPC_IDLE` | 2 | – | – |
| `0xA0` | `OPC_LOCO_SPD` | 4 | slot, spd | – |
| `0xA1` | `OPC_LOCO_DIRF` | 4 | slot, dirf | – |
| `0xA2` | `OPC_LOCO_SND` | 4 | slot, snd | – |
| `0xB0` | `OPC_SW_REQ` | 4 | sw1, sw2 | (cond. LACK) |
| `0xB1` | `OPC_SW_REP` | 4 | sn1, sn2 | – |
| `0xB2` | `OPC_INPUT_REP` | 4 | in1, in2 | – |
| `0xB4` | `OPC_LONG_ACK` | 4 | lopc, ack1 | – |
| `0xB5` | `OPC_SLOT_STAT1` | 4 | slot, stat1 | – |
| `0xB6` | `OPC_CONSIST_FUNC` | 4 | slot, dirf | – |
| `0xB8` | `OPC_UNLINK_SLOTS` | 4 | sl1, sl2 | `E7` |
| `0xB9` | `OPC_LINK_SLOTS` | 4 | sl1, sl2 | `E7` |
| `0xBA` | `OPC_MOVE_SLOTS` | 4 | src, dest | `E7` / LACK |
| `0xBB` | `OPC_RQ_SL_DATA` | 4 | slot, 0 | `E7` |
| `0xBC` | `OPC_SW_STATE` | 4 | sw1, sw2 | LACK |
| `0xBD` | `OPC_SW_ACK` | 4 | sw1, sw2 | LACK |
| `0xBE` | `OPC_EXP_REQ_SLOT` † | 4 | adr | `E6` |
| `0xBF` | `OPC_LOCO_ADR` | 4 | 0, adr | `E7` |
| `0xA3` | `RE_OPC_IB2_F9_F12` † | 4 | slot, mask | – |
| `0xD4` | `OPC_…IB2_SPECIAL` † | 6 | slot, token, mask | – |
| `0xD5` | `OPC_EXP_SEND_FUNCTION…` † | 6 | slot, id, data | – |
| `0xE5` | `OPC_PEER_XFER` | var | 8 data | – |
| `0xE6` | `OPC_EXP_RD_SL_DATA` † | var | expanded slot | – |
| `0xE7` | `OPC_SL_RD_DATA` | var (14) | slot block | – |
| `0xED` | `OPC_IMM_PACKET` | var | DCC packet | LACK |
| `0xEE` | `OPC_EXP_WR_SL_DATA` † | var | expanded slot | LACK |
| `0xEF` | `OPC_WR_SL_DATA` | var (14) | slot block | LACK |

† Post-PE-1.0 (LocoNet 2 / vendor); see §7.4, §11, §12.

LACK fail codes seen in the spec: `B4 3F 00` (no free slot), `B4 3A 00` (illegal
move / no dispatch), `B4 39 00` (invalid link), `B4 30 00` (switch command failed),
`B4 7F xx` (programmer status), `B4 7D xx` (immediate packet status).

---

## Appendix B – Worked byte examples

**Idle / NOP & checksum**

```
83 7C        OPC_GPON  → XOR 0x83^0x7C = 0xFF ✓
81 7E        OPC_BUSY "time burner" NOP (strip & ignore)
```

**Request loco address 3 (short)**

```
BF 00 03 ??  OPC_LOCO_ADR, adrHi=0, adrLo=3
chk = 0xFF ^ (0xBF ^ 0x00 ^ 0x03) = 0xFF ^ 0xBC = 0x43
→ BF 00 03 43
```

**Set speed on slot 5 to 0x10**

```
A0 05 10 ??  OPC_LOCO_SPD
chk = 0xFF ^ (0xA0 ^ 0x05 ^ 0x10) = 0xFF ^ 0xB5 = 0x4A
→ A0 05 10 4A
```

**Set DIRF on slot 5: forward (0x20) + F0 (0x10)**

```
A1 05 30 ??  OPC_LOCO_DIRF, DIRF = 0x20|0x10 = 0x30
chk = 0xFF ^ (0xA1 ^ 0x05 ^ 0x30) = 0xFF ^ 0x94 = 0x6B
→ A1 05 30 6B
```

**Request slot 5 data**

```
BB 05 00 ??  OPC_RQ_SL_DATA
chk = 0xFF ^ (0xBB ^ 0x05 ^ 0x00) = 0xFF ^ 0xBE = 0x41
→ BB 05 00 41
```

**Set F9 on short address 3 via OPC_IMM_PACKET (§11.1, §19.2)**

```
DCC packet (no error byte): 03 A1        (0xA0 | F9-mask 0x01)
REPS = 0x23   (#bytes=2 → 010 in bits6-4; 4 repeats → 011 in bits2-0)
DHI  = 0x20   (fixed 001 high bits; neither 0x03 nor 0x21 has D7 set)
IM1  = 0x03,  IM2 = 0xA1 & 0x7F = 0x21
ED 0B 7F 23 20 03 21 00 00 00 00 ??
```

**Set F13 on long address 1234 via OPC_IMM_PACKET (§11.1)**

```
1234 = 0x04D2 → addr bytes: C0|0x04 = 0xC4, 0xD2
DCC packet: C4 D2 DE 01   (0xDE group byte + F13-mask 0x01)  → 4 bytes
REPS = 0x43   (#bytes=4 → 100; 4 repeats → 011)
DHI: fixed 0x20 | 0xC4(D7=1)→b0 | 0xD2(D7=1)→b1 | 0xDE(D7=1)→b2 = 0x27
IM1=0x44, IM2=0x52, IM3=0x5E, IM4=0x01
ED 0B 7F 43 27 44 52 5E 01 00 00 ??
```

**LoconetOverTcp wire (§20)**

```
→ SEND A0 05 10 4A         (client requests speed)
← RECEIVE A0 05 10 4A      (bus echo)
← SENT OK                  (server confirms)
```

---

Related: [`z21.md`](./z21.md) (Z21 LAN), command-station docs
[`commandstations/dr5000.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/dr5000.md),
[`commandstations/rb1110.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/rb1110.md), device
[`devices/uhlenbrock-63120.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/devices/uhlenbrock-63120.md), and the LocoNet
bring-up set [`hardware/`](https://github.com/dcc-bigfred/docs/blob/main/content/en/specs/hardware/README.md).
