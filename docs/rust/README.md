# Rust libraries — Z21 and WiThrottle codecs

The production Rust crates encode and decode wire bytes. They are `#![no_std]`, have no `alloc`, and **do not open sockets**. Firmware (LongFred) or a `std` host owns UDP/TCP (`embassy-net`, `std::net`, …) and feeds the codec.

Same wire semantics as the [Go `commandstation` client](../go/README.md). There is no connected `NewZ21Roco` / `NewWiThrottle` equivalent yet — `dcc-proto-commandstation` is an experimental stub.

| Crate | Role |
|-------|------|
| [`dcc-proto-z21`](../../rust/z21) | Z21 LAN codec (UDP datagrams) |
| [`dcc-proto-withrottle`](../../rust/withrottle) | WiThrottle codec (TCP lines) |
| `dcc-proto-commandstation` | Experimental `Station` trait + `Stub` (no I/O) |
| `dcc-proto-loconet` | Experimental gateway stub (real gateway is Go) |
| `dcc-proto-z21-server` / `dcc-proto-withrottle-server` | Experimental `std` listeners |

Requires Rust ≥ 1.75.

## Installation

From this repository (crates are not published to crates.io):

```toml
[dependencies]
dcc-proto-z21 = { git = "https://github.com/dcc-bigfred/proto.git", path = "rust/z21" }
dcc-proto-withrottle = { git = "https://github.com/dcc-bigfred/proto.git", path = "rust/withrottle" }
```

Path dependency inside the workspace:

```toml
dcc-proto-z21 = { path = "../z21" }
```

Output goes into a bounded `heapless` buffer (`WireBuf`, 256 bytes). `Error::BufferFull` means the buffer had no remaining capacity.

The client shape is the same for both protocols:

1. `on_connect(&mut out)` — bytes to send after the socket is up
2. `on_bytes(input, &mut emit)` — parse inbound, call `emit` for each event
3. `encode(&cmd, &mut out)` — append one command (host writes `out` to the socket)

## Connecting

### Z21 (UDP, port 21105)

`Client::on_connect` writes `LAN_GET_SERIAL_NUMBER` plus `LAN_SET_BROADCASTFLAGS` (`0x00010001`: driving + all locos).

```rust
use dcc_proto_z21 as z21;
use std::net::UdpSocket;

fn connect_z21(addr: &str) -> std::io::Result<(UdpSocket, z21::Client)> {
    let sock = UdpSocket::bind("0.0.0.0:0")?;
    sock.connect(addr)?; // e.g. "192.168.0.111:21105"
    let mut cli = z21::Client::new();
    let mut hello = z21::WireBuf::new();
    cli.on_connect(&mut hello).expect("buffer");
    sock.send(&hello)?;
    Ok((sock, cli))
}
```

Inbound serial replies and `LAN_X_LOCO_INFO` arrive as `Event::Serial` / `Event::LocoInfo`:

```rust
let mut buf = [0u8; 256];
let n = sock.recv(&mut buf)?;
cli.on_bytes(&buf[..n], &mut |ev| match ev {
    z21::Event::Serial(s) => println!("Z21 serial {s}"),
    z21::Event::LocoInfo(info) => {
        println!("loco {} speed {} fwd {}", info.addr, info.speed, info.forward)
    }
});
```

### WiThrottle (TCP, default port 12090)

Works with JMRI, DCC-EX, LNWI, RB1110. `Client::new(name, id)` becomes `N` / `HU`; `on_connect` also enables heartbeat (`*+`).

Acquire a locomotive before speed, functions, or e-stop (`M0+`).

```rust
use dcc_proto_withrottle as wt;
use std::io::Write;
use std::net::TcpStream;

fn connect_wt(addr: &str) -> std::io::Result<(TcpStream, wt::Client)> {
    let mut stream = TcpStream::connect(addr)?; // e.g. "192.168.0.42:12090"
    let mut cli = wt::Client::new("my-app", "app-01");
    let mut hello = wt::WireBuf::new();
    cli.on_connect(&mut hello).expect("buffer");
    stream.write_all(&hello)?;
    Ok((stream, cli))
}

fn send(stream: &mut TcpStream, cli: &wt::Client, cmd: wt::Command) -> std::io::Result<()> {
    let mut out = wt::WireBuf::new();
    cli.encode(&cmd, &mut out).expect("buffer");
    stream.write_all(&out)
}
```

Typical handshake burst: `Event::Protocol` (`VN…`), `Event::Heartbeat` (`*…`), `Event::TrackPower`.

## Driving

### Z21 — `Command::SetSpeed`

`steps` is `14`, `28`, or `128` (or the protocol nibble `0` / `2` / `3`). Speed `0` = stop; `1` = emergency stop; `2`…`127` = driving.

```rust
let mut out = z21::WireBuf::new();
cli.encode(
    &z21::Command::SetSpeed {
        addr: 3,
        speed: 50,
        forward: true,
        steps: 128,
    },
    &mut out,
)?;
sock.send(&out)?;

out.clear();
cli.encode(
    &z21::Command::SetSpeed {
        addr: 3,
        speed: 0,
        forward: true,
        steps: 128,
    },
    &mut out,
)?;
sock.send(&out)?; // normal stop
```

Query current state with `Command::GetLocoInfo { addr }` and parse the reply as `Event::LocoInfo` (or `z21::parse_loco_info`).

### WiThrottle — acquire, direction, speed

Speed and direction are separate lines (`M0A…V` / `M0A…R`). Addresses ≥ 128 use the long key (`L128`).

```rust
send(&mut stream, &cli, wt::Command::Acquire { addr: 3 })?;
send(
    &mut stream,
    &cli,
    wt::Command::SetDirection {
        addr: 3,
        forward: true,
    },
)?;
send(
    &mut stream,
    &cli,
    wt::Command::SetSpeed {
        addr: 3,
        speed: 50,
    },
)?;
send(
    &mut stream,
    &cli,
    wt::Command::SetSpeed {
        addr: 3,
        speed: 0,
    },
)?; // normal stop
```

Inbound `M0A` updates arrive as `Event::Speed` and `Event::Direction`.

## Auxiliary functions

Function `0` is the headlight (F0). `on` is the desired state (true = on, false = off).

### Z21 — `LAN_X_SET_LOCO_FUNCTION` (F0–F31)

```rust
cli.encode(
    &z21::Command::SetFunction {
        addr: 3,
        func: 0,
        on: true,
    },
    &mut out,
)?;
```

`LocoInfo.functions` is a bitmask (bit 0 = F0).

### WiThrottle — `M0A…f` (F0–F28, server-dependent)

```rust
send(
    &mut stream,
    &cli,
    wt::Command::SetFunction {
        addr: 3,
        func: 0,
        on: true,
    },
)?;
```

There is no CV programming in either codec (no `ReadCV` / `WriteCV` on the wire API).

## Emergency stop

Per-locomotive, not a global layout e-stop.

| Protocol | On-the-wire |
|----------|-------------|
| Z21 | `LAN_X_SET_LOCO_DRIVE` with V=1 (`SetSpeed { speed: 1, … }`) |
| WiThrottle | `M0A«key»<;>X` (`Command::EmergencyStop`) |

```rust
// Z21 — keep the current direction bit on the wire
cli.encode(
    &z21::Command::SetSpeed {
        addr: 3,
        speed: 1,
        forward: true,
        steps: 128,
    },
    &mut out,
)?;

// WiThrottle
send(&mut stream, &cli, wt::Command::EmergencyStop { addr: 3 })?;
```

## Track power

First-class on both `Command` enums (not a separate optional trait).

```rust
// Z21 — LAN_X_SET_TRACK_POWER_ON / OFF
cli.encode(&z21::Command::TrackPower { on: true }, &mut out)?;

// WiThrottle — PPA1 / PPA0
send(&mut stream, &cli, wt::Command::TrackPower { on: true })?;
send(&mut stream, &cli, wt::Command::TrackPower { on: false })?;
```

WiThrottle also reports layout power inbound as `Event::TrackPower { on }`.

## Complete example (Z21 over `std::net`)

```rust
use dcc_proto_z21 as z21;
use std::net::UdpSocket;
use std::time::Duration;

fn main() -> std::io::Result<()> {
    let sock = UdpSocket::bind("0.0.0.0:0")?;
    sock.connect("192.168.0.111:21105")?;
    sock.set_read_timeout(Some(Duration::from_secs(2)))?;

    let mut cli = z21::Client::new();
    let mut out = z21::WireBuf::new();
    cli.on_connect(&mut out).unwrap();
    sock.send(&out)?;

    out.clear();
    cli.encode(
        &z21::Command::SetSpeed {
            addr: 42,
            speed: 30,
            forward: true,
            steps: 128,
        },
        &mut out,
    )
    .unwrap();
    sock.send(&out)?;

    out.clear();
    cli.encode(
        &z21::Command::SetFunction {
            addr: 42,
            func: 0,
            on: true,
        },
        &mut out,
    )
    .unwrap();
    sock.send(&out)?;

    out.clear();
    cli.encode(
        &z21::Command::SetSpeed {
            addr: 42,
            speed: 1,
            forward: true,
            steps: 128,
        },
        &mut out,
    )
    .unwrap();
    sock.send(&out)?; // e-stop

    Ok(())
}
```

## Experimental `Station` stub

`dcc-proto-commandstation` exposes a `Station` trait aligned with Go (`set_speed`, `get_speed`, `send_fn`, `read_cv`, `write_cv`, `emergency_stop`). Only `Stub` implements it: in-memory speed/direction, CV ops return `Unsupported`, e-stop is speed 0. It does not dial a command station.

## Not supported in Rust yet

- Connected LocoNet client (serial / LbServer / binary TCP) — use Go
- LAN autodetection
- Slot manager, state observer channels, CV programming on the wire
- A production Z21/WiThrottle **server** (the `*-server` crates are experimental)

## Tests and further reading

- Golden vectors: `testdata/z21/*.json`, `testdata/withrottle/*.json` (shared with Go)
- Codec unit tests in `rust/z21` and `rust/withrottle`
- Network interop (Rust codec ↔ Go `Listen`): `make test-interop`
- Protocol specifications: [`docs/z21.md`](../z21.md), [`docs/withrottle.md`](../withrottle.md)
- Architecture: [`ARCHITECTURE.md`](../../ARCHITECTURE.md)
- Run crate tests: `make test-rust` from the repository root
