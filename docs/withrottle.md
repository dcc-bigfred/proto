# WiThrottle Protocol Specification

> Technical reference for the **WiThrottle™** network protocol as used by mobile
> throttle apps (Engine Driver, WiThrottle for iOS, …) and JMRI-class servers.
>
> Sources:
> - **JMRI WiThrottle Protocol** —
>   [Protocol.shtml](https://www.jmri.org/help/en/package/jmri/jmrit/withrottle/Protocol.shtml).
>   Normative description of commands, delimiters, and the MultiThrottle model
>   implemented by JMRI's `jmri.jmrit.withrottle` package.
> - **flash62au/WiThrottleProtocol** (Arduino/ESP32 client library) —
>   [github.com/flash62au/WiThrottleProtocol](https://github.com/flash62au/WiThrottleProtocol),
>   [library docs](https://flash62au.github.io/WiThrottleProtocol/index.html).
>   De-facto reference for client behaviour: heartbeat handling, command pacing,
>   MultiThrottle command builders, and parsing edge cases on real hardware
>   (JMRI, Digitrax LNWI, RailBOX RB1110).
>
> Conventions:
> - Commands and responses are **plain ASCII text**, one logical message per line.
> - Examples show the payload only; each line is terminated by a newline (see §2.2).
> - Loco addresses are written `Snnn` (short, 1–127) or `Lnnn` (long, 128–10239).
> - WiThrottle™ is a trademark; message formats reproduced here are for
>   interoperability.

## [Overview & design philosophy](#1-overview--design-philosophy)

## [Transport & discovery](#2-transport--discovery)

## [Framing & delimiters](#3-framing--delimiters)

## [Connection lifecycle](#4-connection-lifecycle)

## [Initial server messages](#5-initial-server-messages)

## [Client registration & heartbeat](#6-client-registration--heartbeat)

## [MultiThrottle model](#7-multithrottle-model)

## [Locomotive acquisition & release](#8-locomotive-acquisition--release)

## [Throttle actions](#9-throttle-actions)

## [Server property notifications](#10-server-property-notifications)

## [Track power](#11-track-power)

## [Turnouts / points](#12-turnouts--points)

## [Routes](#13-routes)

## [Consists (roster & advanced)](#14-consists-roster--advanced)

## [Fast clock](#15-fast-clock)

## [Alerts, server identity & misc](#16-alerts-server-identity--misc)

## [Deprecated & auxiliary commands](#17-deprecated--auxiliary-commands)

## [Client implementation notes](#18-client-implementation-notes)

## [BigFred mapping](#19-bigfred-mapping)

## [Appendix A – Command quick reference](#appendix-a--command-quick-reference)

## [Appendix B – Worked examples](#appendix-b--worked-examples)

---

## 1 Overview & design philosophy

WiThrottle is a **client–server**, **line-oriented text protocol** over **TCP/IP**.
A **server** (JMRI, Digitrax LNWI, RailBOX RB1110, …) owns the connection to the
DCC command station; **clients** (mobile throttles, hardware panels, automation
scripts) send terse single-line commands and receive asynchronous updates.

Design properties:

| Property | Detail |
|----------|--------|
| Encoding | 7-bit-safe ASCII (no binary framing) |
| Direction | Full duplex; server may push state without a prior request |
| State model | **MultiThrottle** — up to several logical throttle instances per TCP session, each holding one or more loco addresses |
| Addressing | DCC short (`S`) / long (`L`) prefixes on numeric addresses |
| Speed | NMRA 128-step encoding: **0** = stop, **1** = emergency stop, **2…126** = speed |
| Functions | **F0–F31** (protocol 2.0); momentary vs latching is server/roster dependent |
| Resilience | Optional **heartbeat** with server-side emergency stop on timeout |

Unlike LocoNet (§ peer bus) or Z21 LAN (§ binary UDP), WiThrottle is intentionally
simple for mobile apps: no checksums, no slot numbers — the server maps loco
addresses to command-station resources internally.

---

## 2 Transport & discovery

### 2.1 TCP connection

| Parameter | Typical value |
|-----------|---------------|
| Transport | **TCP** |
| Port | **12090** (de-facto standard; JMRI default, RailBOX RB1110 since FW ≥ 8.0) |
| Session | One TCP connection per client device |

JMRI and most servers accept a single long-lived connection per client. The client
should send `Q` before closing so the server can release throttles promptly.

### 2.2 Line termination

Each command or response is one line. A line ends with:

| Sequence | Hex |
|----------|-----|
| Line feed | `0x0A` |
| Carriage return | `0x0D` |
| CR + LF | `0x0D 0x0A` |

**Both peers must accept any of the above** as end-of-line. JMRI commonly emits
**two** consecutive newlines after some responses; robust clients trigger on the
first terminator and discard the second empty line
([WiThrottleProtocol.cpp](https://github.com/flash62au/WiThrottleProtocol/blob/master/src/WiThrottleProtocol.cpp)
`check()`).

There is **no** escape mechanism — command text must not contain raw newline
characters.

### 2.3 Service discovery (mDNS / Bonjour)

Clients may locate servers with a multicast DNS browse for:

```text
_withrottle._tcp.local
```

Discovery is **best-effort** (depends on OS, firewall, and VLAN isolation). Production
clients should always allow manual **host + port** entry. Engine Driver and
WiThrottle for iOS both support mDNS and static configuration.

### 2.4 Known server deployments

| Server | Port | Notes |
|--------|------|-------|
| JMRI | 12090 (configurable) | Reference implementation |
| Digitrax LNWI | 12090 | WiThrottle bridge to LocoNet; may emit `AT+CIPSENDBUF=` noise and `PFC` (§16.4) |
| RailBOX RB1110 | 12090 | Alongside Z21 UDP **21105**, LocoNet-TCP **5560**, LenzLAN **5550** — see [RB1110 §6.3](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/rb1110.md) |

---

## 3 Framing & delimiters

WiThrottle packs structured data into flat strings using fixed **three-character**
delimiters (defined in
[WiThrottleProtocol.h](https://github.com/flash62au/WiThrottleProtocol/blob/master/src/WiThrottleProtocol.h)):

| Delimiter | Constant | Role |
|-----------|----------|------|
| `]\[` | `ENTRY_SEPARATOR` | Separates **array elements** (roster entries, function labels, consist members, …) |
| `}\{` | `SEGMENT_SEPARATOR` | Separates **fields within one element** |
| `<;>` | `PROPERTY_SEPARATOR` | Separates **major command parts** (MultiThrottle sub-commands) |
| `<:>` | (consist commands) | Separates consist name vs address in `RC+` / `RC-` |

### 3.1 Roster example

Two roster entries:

```text
RL2]\[RGS 41}|{41}|{L]\[Test Loco}|{1234}|{L
```

Parsed as:

| Entry | Name | Address | Length flag |
|-------|------|---------|-------------|
| 1 | `RGS 41` | 41 | `L` (long address) |
| 2 | `Test Loco` | 1234 | `L` |

The count after `RL` (`2`) must match the number of `]\[`-delimited entries.
`S` vs `L` in the third field indicates short vs long DCC address format for
that roster entry.

### 3.2 MultiThrottle example

Add long-address loco 341 to throttle `0`, referencing roster entry `D&RGW 341`:

```text
M0+L341<;>ED&RGW 341
```

The part before `<;>` is the **acquisition key** (`+L341`); the part after is
the **address selector** passed to the server's throttle controller (`ED&RGW 341`).

---

## 4 Connection lifecycle

Typical client sequence:

```mermaid
sequenceDiagram
  participant C as Client
  participant S as WiThrottle Server

  C->>S: TCP connect :12090
  Note over S: May defer initial burst until HU/N
  C->>S: HU«uniqueDeviceId»
  C->>S: N«deviceName»
  S->>C: *«heartbeatSeconds»
  S->>C: VN2.0, RL…, PPA…, … (initial burst)
  C->>S: *+  (enable heartbeat monitoring)
  loop Session
    C->>S: M0+S3<;>S3  (acquire loco)
    S->>C: M0+S3<;> … M0AS3<;>V0 … (state dump)
    C->>S: M0A*<;>V30   (set speed)
    S->>C: M0AS3<;>V30  (notification, if changed elsewhere)
    C->>S: *  (heartbeat)
  end
  C->>S: Q
  C->>S: TCP close
```

**Ordering notes:**

- Some servers (including JMRI) defer the §5 initial burst until the client sends
  `HU` and/or `N`.
- `HU` must be **unique per simultaneous connection** — duplicate IDs may be
  rejected. Mobile apps typically use a random UUID string.
- `N` sets the human-readable name shown in the server's WiThrottle window.
- The server replies to `N` with `*«seconds»` — the emergency-stop timeout if no
  further commands arrive (§6).

---

## 5 Initial server messages

After connect (and often after `HU` / `N`), the server may send one line per
topic. Lines can arrive in any order; clients should parse by **two- or
three-letter prefix**.

### 5.1 Protocol version — `VN`

```text
VN2.0
```

| Field | Meaning |
|-------|---------|
| `VN` | Version announcement |
| `2.0` | Protocol level |

Engine Driver requires **≥ 2.0**. Syntax before 2.0 used deprecated single-throttle
`T` / `S` / `MT` prefixes (§17).

### 5.2 Roster list — `RL`

```text
RL0
RL2]\[RGS 41}|{41}|{L]\[Test Loco}|{1234}|{L
```

| Field | Meaning |
|-------|---------|
| `RL` | Roster list |
| `0` / `n` | Entry count |
| `]\[` … | Per-entry: `name}|{address}|{S\|L` |

### 5.3 Track power — `PPA`

```text
PPA1
```

| Value | Meaning |
|-------|---------|
| `0` | Off |
| `1` | On |
| `2` | Unknown |

Also used as a **client → server** command (§11).

### 5.4 Turnout captions — `PTT`

```text
PTT]\[Turnouts}|{Turnout]\[Closed}|{2]\[Thrown}|{4]\[Unknown}|{1]\[Inconsistent}|{8
```

Provides human-readable labels and numeric **state codes** for turnout feedback
(§12). The outer `]\[` pair wraps the global title and per-state entries.

### 5.5 Turnout list — `PTL`

```text
PTL]\[LT12}|{Rico Station N}|{1]\[LT324}|{Rico Station S}|{2
```

| Field | Meaning |
|-------|---------|
| System name | JMRI system name (e.g. `LT12`) — used in `PTA` commands |
| User name | Display label |
| State | Current state code (see §12.3) |

`PTL` alone (no entries) means zero turnouts.

### 5.6 Route captions — `PRT` and list — `PRL`

Same pattern as turnouts:

```text
PRT]\[Routes}|{Route]\[Active}|{2]\[Inactive}|{4]\[Unknown}|{0]\[Inconsistent}|{8
PRL]\[IR:AUTO:0001}|{Rico Main}|{2
```

Route state codes: `2` = Active, `4` = Inactive, `8` = Inconsistent, `0` /
`1` = Unknown (server-dependent).

### 5.7 Consist list — `RCC` / `RCL` / `RCD`

Header (treat `RCC` and `RCL` identically):

```text
RCC2
```

Per-consist lines (one line each — **not** the same `]\[` roster pattern):

```text
RCD}|{74(S)}|{74(S)]\[3374(L)}|{true]\[346(L)}|{true
```

| Field | Meaning |
|-------|---------|
| Consist address | e.g. `74(S)` |
| Consist ID | Usually same as address |
| Members | `]\[` array of `address}|{facing` where facing is `true` / `false` |

### 5.8 Web server port — `PW`

```text
PW12080
```

JMRI embedded web UI port (informational for clients that deep-link into JMRI).

---

## 6 Client registration & heartbeat

### 6.1 Device ID — `HU`

```text
HUa1b2c3d4-e5f6-7890-abcd-ef1234567890
```

| Segment | Description |
|---------|-------------|
| `HU` | Hardware / unique client identifier |
| remainder | Opaque string; **must differ** across simultaneous connections |

### 6.2 Device name — `N`

```text
NJohn's Throttle
```

Any text except newline. Server reply:

```text
*10
```

| Reply | Meaning |
|-------|---------|
| `*«n»` | If no command or heartbeat within **n** seconds, server may **E-stop** controlled locos (`0` = heartbeat not required) |

### 6.3 Heartbeat — `*`

| Command | Direction | Meaning |
|---------|-----------|---------|
| `*` | Client → server | "I am alive" |
| `*+` | Client → server | Enable heartbeat monitoring |
| `*−` | Client → server | Disable heartbeat monitoring |
| `*«n»` | Server → client | Configured timeout (after `N`) |

Heartbeat monitoring is **off** until the client sends `*+`. While enabled, **any**
valid client command (including `*`) resets the watchdog. The
[flash62au](https://github.com/flash62au/WiThrottleProtocol) client alternates `*`
with re-sending `N«name»` to force a server response when half the period has
elapsed.

### 6.4 Quit — `Q`

```text
Q
```

Client announces disconnect; server should release throttles tied to this session.

---

## 7 MultiThrottle model

Protocol 2.0 routes all throttle traffic through **`M`** (MultiThrottle) commands.
The character **immediately after `M`** selects the throttle instance:

| ID | Alias | Numeric index |
|----|-------|---------------|
| `0` … `9` | — | 0 … 9 |
| `T` | legacy default | 0 |
| `S` | second throttle (legacy) | 1 |
| `G` | third throttle (legacy) | 2 |

Each MultiThrottle instance holds **zero or more** loco addresses (an on-the-fly
"consist" from the protocol's point of view — distinct from NMRA advanced consists
in §14). Engine Driver uses `0`–`6`; the flash62au library supports **6** instances
(`0`–`5` plus `T`).

**Important:** do not mix legacy `T`-prefixed APIs with `0`-prefixed MultiThrottle
APIs in one program — legacy code uses defunct throttle id `T`, modern code uses
`0` ([library README](https://github.com/flash62au/WiThrottleProtocol)).

### 7.1 MultiThrottle command shape

```text
M«throttleId»«operation»«locoKey»<;>«payload»
```

| Operation (3rd char) | Meaning |
|----------------------|---------|
| `+` | Add loco (§8.1) |
| `−` | Remove loco (§8.2) |
| `S` | Steal prompt / steal request (§8.3) |
| `A` | Throttle action on `locoKey` (§9) |
| `L` | Function label list from roster (§10.2) |

`locoKey` is normally `Snnn`, `Lnnn`, or `*` (all locos on this MultiThrottle).

---

## 8 Locomotive acquisition & release

### 8.1 Add locomotive — `M…+`

```text
M0+S3<;>S3
M0+L341<;>ED&RGW 341
```

| Part | Meaning |
|------|---------|
| `+S3` / `+L341` | Acquisition **key** used in later `A` commands |
| `<;>` | Separator |
| `S3` / `ED&RGW 341` | Address selector: raw `S`/`L` address **or** roster entry `E«id»` |

The key address **must match** the address chosen in the selector; mismatch is
undefined (behaviour varies by server).

**Server reply** (verbose — multiple lines), when successful:

```text
M0+S3<;>
M0LS3<;>]\[Headlight]\[Bell]\[…     (only if loco is in roster)
M0AS3<;>F00
M0AS3<;>F01
…
M0AS3<;>F028
M0AS3<;>V0
M0AS3<;>R1
M0AS3<;>s1
```

Roster entries include a function-name array (`M…L…`, §10.2). Ad-hoc addresses
skip the label list but still receive `F` / `V` / `R` / `s` states.

### 8.2 Remove locomotive — `M…−`

```text
M0-S3<;>r
M0-*<;>r
```

| Part | Meaning |
|------|---------|
| `−S3` | Remove loco with key `S3` |
| `−*` | Remove **all** locos on this MultiThrottle |
| `<;>r` | Release command (`r`); `d` = dispatch (§9.8) |

Server typically confirms:

```text
M0-S3<;>
```

### 8.3 Steal — `M…S`

Used with Digitrax systems when a loco is already controlled elsewhere.

| Step | Direction | Example |
|------|-----------|---------|
| 1 | Client → server | `M0+S3<;>S3` (normal acquire) |
| 2 | Server → client | `M0SS3<;>S3` (steal required) |
| 3 | Client → server | `M0SS3<;>S3` (steal confirm) |

Requires JMRI ≥ 4.10 for JMRI-backed servers.

---

## 9 Throttle actions

Actions are sent as the second part of an `M…A` command:

```text
M0A*<;>V30
M0AS3<;>F112
M0AL341<;>R0
```

`locoKey` before `<;>` may be `*` (all locos on throttle) or a specific `S`/`L`
key. The payload after `<;>` is a **letter + value** throttle sub-command.

### 9.1 Sub-command summary

| Cmd | Format | Description |
|-----|--------|-------------|
| `V` | `V«speed»` | Speed **0…126** (§9.2) |
| `R` | `R«dir»` | Direction: `0` = reverse, non-`0` = forward |
| `F` | `F«state»«fn»` | Function **press/release** (§9.3) |
| `f` | `f«state»«fn»` | **Force** function on/off (§9.4) |
| `m` | `m«mode»«fn»` | Momentary (`1`) vs latching (`0`) override (§9.5) |
| `s` | `s«mode»` | Speed-step mode (§9.6) |
| `X` | `X` | Emergency stop |
| `I` | `I` | Idle — speed 0 (normal stop) |
| `q` | `qV` / `qR` | Query speed / direction (§9.7) |
| `C` / `c` | `C«lead»` / `c«lead»` | Set consist **lead** for function routing (§9.9) |
| `r` / `d` | `r` / `d` | Release / dispatch (§9.8) |
| `E` | `E«rosterId»` | Select address from roster entry |
| `L` / `S` | `L«addr»` / `S«addr»` | Set long / short address directly |
| `Q` | `Q` | Quit this throttle instance |

Function numbers are **0–31** without leading zeros (`F10`, not `F010`).

### 9.2 Speed — `V`

```text
M0A*<;>V30
```

| Value | Meaning |
|-------|---------|
| `0` | Stop |
| `1` | Emergency stop encoding on wire |
| `2…126` | Increasing speed (`126` = max) |

During initialization the server may send **negative** speed values to indicate
E-stop state (JMRI); clients should treat that as stopped / E-stop.

### 9.3 Function press/release — `F`

Momentary UI buttons send **pairs**:

```text
M0A*<;>F112
M0A*<;>F012
```

| Field | Meaning |
|-------|---------|
| `1` / `0` after `F` | Press / release (not the same as on/off for latching functions) |
| digits after | Function number |

The server maps press/release to the correct DCC behaviour (momentary whistle vs
toggling headlight).

### 9.4 Force function — `f`

```text
M0A*<;>f112
```

Sets absolute on (`1`) / off (`0`) regardless of prior state. Server emits `M…A`
notification only when the state actually changes.

### 9.5 Momentary vs latching — `m`

```text
M0A*<;>m112
M0A*<;>m012
```

Overrides roster defaults: `m1«fn»` = momentary, `m0«fn»` (or any non-`1`) =
latching. JMRI global preference "F2 always momentary" overrides for F2.

### 9.6 Speed-step mode — `s`

```text
M0AS3<;>s1
```

| Value | Step mode |
|-------|-----------|
| `1` | 128 speed steps |
| `2` | 28 speed steps |
| `4` | 27 speed steps |
| `8` | 14 speed steps |

### 9.7 Query — `q`

```text
M0A*<;>qV
M0A*<;>qR
```

Server answers with `M…A` notifications (`V…`, `R…`) per loco.

### 9.8 Release & dispatch — `r` / `d`

Usually sent as part of `M…−` (§8.2). On many systems **release** and
**dispatch** are equivalent; prefer `r` when unsure.

### 9.9 Consist lead routing — `C` / `c`

```text
M0AL341<;>CL346
```

Directs function commands to the **lead** loco `L346` when not using CV21/CV22
advanced consist mapping.

---

## 10 Server property notifications

Asynchronous `M` lines inform clients about changes (from other throttles, panels,
or automation):

```text
M0AL341<;>F10
M0AL341<;>V23
M0AL341<;>R1
M0AL341<;>s1
```

### 10.1 Notification shape — `M…A`

```text
M«id»A«locoKey»<;>«property»
```

| `property` prefix | Meaning |
|-------------------|---------|
| `F«state»«fn»` | Function off/on |
| `V«speed»` | Speed |
| `R«dir»` | Direction |
| `s«mode»` | Speed-step mode |

### 10.2 Function labels — `M…L`

```text
M0LL7407<;>]\[Lights]\[Bell]\[Whistle]\[…
```

Returned after roster acquire. Delimiters: leading `]\[`, between labels `]\[`,
trailing `]\[`. Index **n** in the array maps to **Fn** (F0 = first label).

### 10.3 Add/remove notifications — `M…+` / `M…−`

```text
M0+S3<;>
M0-S3<;>
```

Empty payload after `<;>` confirms add/remove events.

### 10.4 Steal prompt — `M…S`

See §8.3.

---

## 11 Track power

### 11.1 Server → client

Announced in the initial `PPA` line (§5.3) and on changes.

### 11.2 Client → server

```text
PPA1
PPA0
```

Sets track power on/off where the server supports it. Not all command stations
expose power control through WiThrottle.

---

## 12 Turnouts / points

### 12.1 Client request — `PTA`

```text
PTACLT92
PTATLT92
PTA2LT92
```

| Segment | Meaning |
|---------|---------|
| `PTA` | Turnout command prefix |
| Action | `C` = closed, `T` = thrown, `2` = toggle |
| Name | System name (`LT92`) or numeric index (server picks default connection) |

JMRI may **create** unknown turnouts when preference allows; errors return `HM…`
(§16.1).

### 12.2 Server notification — `PTA`

```text
PTA2LT92
```

| State digit | Meaning |
|-------------|---------|
| `2` | Closed |
| `4` | Thrown |
| `1` | Unknown |
| `8` | Inconsistent |

Broadcast for **all** turnout changes on the layout, not only those requested by
this client.

---

## 13 Routes

### 13.1 Client request — `PRA`

```text
PRA2IO_RESET_LAYOUT
```

| Segment | Meaning |
|---------|---------|
| `PRA` | Route command prefix |
| `2` | Set / activate route |
| Name | Route system name |

### 13.2 Server notification — `PRA`

```text
PRA2IO_RESET_LAYOUT
```

| State digit | Meaning |
|-------------|---------|
| `2` | Active |
| `4` | Inactive |
| `8` | Inconsistent |

---

## 14 Consists (roster & advanced)

Two layers:

1. **MultiThrottle multi-loco** (§7) — protocol-level list on one TCP throttle; no
   CV programming.
2. **NMRA advanced consists** — `RC` commands manipulating decoder CV19 and CV21/22.

### 14.1 Advanced consist commands — `RC`

All start with `RC`:

| Cmd | Example | Purpose |
|-----|---------|---------|
| `RC+` | `RC+<;>S74<;>My consist<:>L341<;>true` | Create / add loco to consist |
| `RC-` | `RC-<;>S74<:>L341` | Remove one loco |
| `RCP` | `RCP<;>S74<:>L346<;>L3374` | Reorder locos (lead first) |
| `RCR` | `RCR<;>S74` | Delete entire consist |
| `RCF` | (see JMRI doc) | Program CV21/CV22 function behaviour |

`RC+` fields (JMRI):

```text
RC+<;>S74<;>My consist<:>L341<;>true
```

| Field | Meaning |
|-------|---------|
| Consist address | `S74` |
| Consist name | `My consist` |
| `<:>` | Separates name from first loco |
| Loco address | `L341` |
| Direction | `true` = normal, `false` = reversed in consist |

Initial `RCC` / `RCD` lines (§5.7) describe existing consists.

---

## 15 Fast clock

### 15.1 Server → client — `PFT`

```text
PFT65871<;>4
PFT1550686525<;>4.0
PFT1550681224<;>0.0
```

| Field | Meaning |
|-------|---------|
| seconds | Integer seconds since **1970-01-01 00:00:00** *fast-clock* calendar (timezone differs from Unix UTC; use modulo **86400** for time-of-day only) |
| `<;>` | Separator |
| ratio | Scale factor (`4` = 4× real time). **`0` / `0.0` = stopped** |

Sent when rate or time changes, and roughly once per **fast-clock minute** while
running. Digitrax LNWI may use the range **0…86400** for time-of-day only.

**Extracting HH:MM:SS** (JMRI style):

```text
seconds mod 86400 → seconds since midnight
```

Example: `1607855025 mod 86400 = 37425` → 10:23:45.

---

## 16 Alerts, server identity & misc

### 16.1 Alerts & info — `HM` / `Hm`

```text
HMJMRI: address 'L23' not allowed as Long
HmTrain 42 approaching station
```

| Prefix | Use |
|--------|-----|
| `HM` | Error / alert (show to user) |
| `Hm` | Informational |

No embedded newlines.

### 16.2 Server type — `HT` / `Ht`

```text
HTJMRI
HtJMRI v4.19.8 My JMRI Railroad
```

Known `HT` types include `JMRI`, `Digitrax`, `MRC`. Clients may branch on these.

### 16.3 Unknown commands

Clients **must ignore** unrecognized lines and continue reading. Servers likewise
ignore unknown client commands.

### 16.4 `PFC` (LNWI)

Digitrax LNWI may send a `PFC` line after connect; purpose undocumented in JMRI
help. Treat as ignorable.

### 16.5 `AT+CIPSENDBUF=` (LNWI artefact)

The flash62au parser strips leading `AT+CIPSENDBUF=` noise occasionally prepended
by LNWI firmware before the real WiThrottle payload.

---

## 17 Deprecated & auxiliary commands

### 17.1 Legacy single-throttle prefixes

| Prefix | Status | Replacement |
|--------|--------|---------------|
| `T…` | Deprecated | `M0…` / `M«id»…` |
| `S…` | Deprecated second throttle | `M1…` |
| `MT…` | Deprecated MultiThrottle alias | `M0…` |

Steal examples in older docs use `MT+` / `MTS`; modern clients use `M0+` / `M0S`.

### 17.2 Raw DCC packet — `D`

```text
D«hex bytes…»
```

Sends a **hex-encoded** packet to the command station (implementation-defined).
Rare in mobile clients; useful for scripting through JMRI.

### 17.3 Panel — `P`

Prefix for panel operations (handled by JMRI DeviceServer); not used by standard
mobile throttles.

### 17.4 Roster — `R`

Prefix for roster operations beyond the initial `RL` list (server-specific).

### 17.5 `C` forward

Legacy no-op forwarding to throttle controller — not used in new clients.

---

## 18 Client implementation notes

Practices distilled from
[flash62au/WiThrottleProtocol](https://github.com/flash62au/WiThrottleProtocol)
and field experience:

| Topic | Recommendation |
|-------|----------------|
| Command pacing | Minimum **50 ms** between outbound commands (`connect(stream, 50)`); burst traffic can overwhelm JMRI or LNWI |
| Leading CRLF | Some servers (e.g. WiFi-equipped throttles) expect an extra `\r\n` before each command; configurable via `setCommandsNeedLeadingCrLf()` |
| Buffer size | Cap input lines (library default ≈ 256 bytes); log and discard overlong lines |
| Heartbeat | Call `*+` after reading `*«n»`; send `*` or any command before timeout; consider re-sending `N` to verify server liveness |
| MultiThrottle | Use `0`–`5` consistently; never mix with legacy `T` API |
| Function state | Track `F0`–`F31` locally — server notifications may arrive from other controllers |
| Negative `V` | Treat as E-stop indication during loco acquisition |
| LNWI quirks | Strip `AT+CIPSENDBUF=` prefix; ignore `PFC`; expect occasional garbage lines |
| Unknown lines | Ignore and continue — forward compatibility |

### 18.1 Suggested parser structure

1. Read bytes until CR or LF; ignore duplicate empty lines.
2. Match **longest prefix first** (`PFT`, `PTL`, `M0A`, `VN`, …) — the flash62au
   `processCommand()` ordering is a useful reference.
3. For `M` messages, branch on **character index 2** (`+`, `−`, `S`, `A`, `L`).
4. Split remaining fields on `<;>` then parse action letter (`V`, `F`, …).

---

## 19 BigFred mapping

BigFred implements an **optional inbound WiThrottle TCP server** inside the
`dcc-bus` daemon, symmetric to the Z21 inbound UDP server, so Engine Driver and
physical WiThrottle handsets drive through the same `cmd.Router` as the browser
throttle. The implementation plan is
[`https://github.com/dcc-bigfred/docs/blob/main/content/en/specs/bigfred/plans/withrottle-server.md`](https://github.com/dcc-bigfred/docs/blob/main/content/en/specs/bigfred/plans/withrottle-server.md). Layout control
otherwise uses:

| Path | Protocol | Typical hardware |
|------|----------|------------------|
| `z21` | Z21 LAN UDP **21105** | RailBOX RB1110, Roco Z21 |
| `withrottle` | WiThrottle TCP **12090** | Engine Driver, WiThrottle for iOS |
| `loconet_serial` / `loconet_tcp` | LocoNet | Digikeijs DR5000 + Uhlenbrock 63120 |

The RB1110 also exposes WiThrottle on TCP **12090** — see
[RB1110 §6.3](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/rb1110.md). When BigFred owns the
command station it binds its own WiThrottle port; BigFred and a vendor WiThrottle
server are **separate consumers** of the same command station and do not share
sessions.

### 19.0 Pairing

Authorization uses a **6-digit numeric code** shown in the BigFred UI. The code is
entered on the handset through one of two equivalent paths:

1. **Function keys** — BigFred advertises a sentinel "Pair with BigFred" roster
   entry to unpaired clients; the user acquires it and presses `F1`…`F9` (one
   digit each) or `F10`…`F32` (two digits each) in sequence. This mirrors the Z21
   function-key pairing flow.
2. **Device name** — the user sets the Engine Driver Device Name to the code
   (`N122145`); BigFred matches it on connect.

The `HU` device id is the paired client key (`withrottle:<deviceId>`); sessions
survive TCP reconnect without re-pairing. One pilot per user per command station
is shared across the Z21 and WiThrottle inbound servers.

### 19.1 Conceptual mapping

The realized mapping from WiThrottle commands to `dcc-bus` intents:

| WiThrottle | BigFred / DCC intent |
|------------|----------------------|
| `M…+` / `M…−` | Loco acquire / release (cf. slot dispatch) |
| `M…A*<;>Vn` | `loco.setSpeed` (`1` = emergency stop) |
| `M…A*<;>Fxy` / `fxy` | `loco.setFunction` |
| `M…A*<;>Rx` | Direction bit in `loco.setSpeed` |
| `M…A*<;>X` | Emergency stop (per loco) |
| `PPA` | Track power (if exposed) |
| `PTA` / `PRA` | Accessory / route (not in v1 scope) |
| `PFT` | Fast clock (not in v1 scope) |
| `RC*` | Advanced consists (not in v1 scope) |
| `*«n»` heartbeat | Dead-man switch: E-stop subscribed locos on timeout, then unpair |
| `RL` | Roster built from the paired user's allowed vehicles |

### 19.2 Capability snapshot

| Capability | BigFred |
|------------|---------|
| WiThrottle server (TCP 12090) | ✅ Optional, opt-in per command station (see plan) |
| Pairing (F-key + device-name code) | ✅ Planned |
| Roster from allowed vehicles | ✅ Planned |
| Turnouts / routes / fast clock / advanced consists | ❌ v1 ignores client cmds; not emitted |
| WiThrottle client | ❌ Not implemented |
| Coexist with Engine Driver on RB1110 | ✅ Different ports — Z21 **21105** vs WiThrottle **12090** |
| Replace Engine Driver | N/A — complementary protocols |


---

## Appendix A – Command quick reference

### Client → server

| Prefix | Example | Purpose |
|--------|---------|---------|
| `HU` | `HU«uuid»` | Unique device ID |
| `N` | `NMy Throttle` | Device display name |
| `*` | `*`, `*+`, `*−` | Heartbeat / monitor control |
| `Q` | `Q` | Quit |
| `M…+` | `M0+S3<;>S3` | Acquire loco |
| `M…−` | `M0-*<;>r` | Release loco(s) |
| `M…S` | `M0SS3<;>S3` | Steal loco |
| `M…A` | `M0A*<;>V30` | Speed / dir / functions / … |
| `PPA` | `PPA1` | Track power |
| `PTA` | `PTACLT12` | Turnout closed |
| `PRA` | `PRA2ROUTE1` | Set route |
| `RC+` | `RC+<;>S74<;>name<:>L3<;>true` | Advanced consist add |
| `RC−` | `RC-<;>S74<:>L3` | Consist remove loco |
| `RCP` | `RCP<;>S74<:>L3<;>L4` | Consist reorder |
| `RCR` | `RCR<;>S74` | Delete consist |
| `D` | `D…` | Raw hex to command station |

### Server → client

| Prefix | Example | Purpose |
|--------|---------|---------|
| `VN` | `VN2.0` | Protocol version |
| `RL` | `RL2]\[…` | Roster |
| `PPA` | `PPA1` | Track power state |
| `PTT` / `PTL` | `PTL]\[…` | Turnout labels / list |
| `PRT` / `PRL` | `PRL]\[…` | Route labels / list |
| `RCC` / `RCD` | `RCD}|{…` | Consist list |
| `PW` | `PW12080` | Web port |
| `*` | `*10` | Heartbeat interval |
| `M…` | `M0AS3<;>V25` | Throttle notifications |
| `PFT` | `PFT37425<;>4` | Fast clock |
| `HM` / `Hm` | `HM…` | Alert / info |
| `HT` / `Ht` | `HTJMRI` | Server identity |
| `PTA` | `PTA2LT12` | Turnout feedback |
| `PRA` | `PRA2ROUTE1` | Route feedback |

---

## Appendix B – Worked examples

### B.1 Minimal session (short address 3)

```text
→ HU550e8400-e29b-41d4-a716-446655440000
→ NBigFred Test
← *10
← VN2.0
← PPA1
→ *+
→ M0+S3<;>S3
← M0+S3<;>
← M0AS3<;>F00
← M0AS3<;>V0
← M0AS3<;>R1
← M0AS3<;>s1
→ M0A*<;>V20
→ M0A*<;>R1
→ M0A*<;>F11
→ M0A*<;>F01
→ M0-*<;>r
← M0-S3<;>
→ Q
```

### B.2 Long address with roster entry

```text
→ M0+L341<;>ED&RGW 341
← M0+L341<;>
← M0LL341<;>]\[Headlight]\[Bell]\[Whistle]\[…
← M0AL341<;>F00
…
→ M0AL341<;>V45
```

### B.3 Turnout throw

```text
← PTT]\[Turnouts}|{Turnout]\[Closed}|{2]\[Thrown}|{4
← PTL]\[LT92}|{Station North}|{2
→ PTATLT92
← PTA4LT92
```

### B.4 Fast-clock time extraction

```text
← PFT1607855025<;>4.0
```

```text
1607855025 mod 86400 = 37425 seconds after midnight
37425 s = 10 h 23 min 45 s
```

### B.5 Steal sequence (Digitrax)

| Server | Client |
|--------|--------|
| `M0SS3<;>S3` | |
| | `M0+S3<;>S3` |
| `M0SS3<;>S3` | |
| | `M0SS3<;>S3` |

---

Related: [`z21.md`](./z21.md) (Z21 LAN), [`loconet.md`](./loconet.md) (LocoNet),
[RailBOX RB1110](https://github.com/dcc-bigfred/docs/blob/main/content/en/related/commandstations/rb1110.md) (WiThrottle port 12090),
[JMRI WiThrottle Protocol](https://www.jmri.org/help/en/package/jmri/jmrit/withrottle/Protocol.shtml).
