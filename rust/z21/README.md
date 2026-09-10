# dcc-bigfred-proto-z21

<p align="center">
  <img src="logo.png" alt="bigfred-proto" width="200">
</p>

Z21 LAN **protocol** library: encode and decode UDP datagrams. `#![no_std]`, no `alloc`, no sockets. Firmware or a `std` host owns UDP and writes the `WireBuf` onto the wire.

## Install

```toml
[dependencies]
dcc-bigfred-proto-z21 = "0.1"
heapless = { version = "0.8", default-features = false }
```

Optional RailCom snapshot (RCN-217 DYN via `LAN_RAILCOM_DATACHANGED`):

```toml
dcc-bigfred-proto-z21 = { version = "0.1", features = ["railcom"] }
```

## Usage

```rust
use dcc_bigfred_proto_z21 as z21;

let mut cli = z21::Client::new();
let mut out = z21::WireBuf::new();
cli.on_connect(&mut out)?; // serial probe + broadcast flags

out.clear();
cli.encode(
    &z21::Command::SetSpeed {
        addr: 3,
        speed: 50,
        forward: true,
        steps: 128,
    },
    &mut out,
)?;

out.clear();
cli.encode(
    &z21::Command::SetFunction {
        addr: 3,
        func: 0,
        on: true,
    },
    &mut out,
)?;

// Per-loco e-stop: LAN_X_SET_LOCO_DRIVE with V=1
out.clear();
cli.encode(
    &z21::Command::SetSpeed {
        addr: 3,
        speed: 1,
        forward: true,
        steps: 128,
    },
    &mut out,
)?;

out.clear();
cli.encode(&z21::Command::TrackPower { on: true }, &mut out)?;
```

Parse inbound datagrams with `on_bytes` (`Event::Serial`, `Event::LocoInfo`, `Event::CvResult` / `CvNack` / `CvNackSc`). Speed `0` is a normal stop; `1` is e-stop. CV/POM commands use 1-based NMRA numbers (`Command::CvRead`, `CvWrite`, `PomRead`, `PomWrite`). `LAN_RAILCOM_DATACHANGED` (`0x88`) yields `Event::RailComLoco(addr)` only; `LocoAddress` is high-byte first on the wire. Full speed/QoS telemetry is behind the `railcom` feature (`on_bytes_with_railcom` + [`dcc-bigfred-proto-railcom`](../railcom)); that path keeps **one parser per locomotive** so fields do not leak between addresses.

UDP sketch (`std`):

```rust
use dcc_bigfred_proto_z21 as z21;
use std::net::UdpSocket;

let sock = UdpSocket::bind("0.0.0.0:0")?;
sock.connect("192.168.0.111:21105")?;
let mut cli = z21::Client::new();
let mut hello = z21::WireBuf::new();
cli.on_connect(&mut hello).unwrap();
sock.send(&hello)?;
```

## Docs

- [Rust protocol guide](https://github.com/dcc-bigfred/proto/blob/main/docs/rust/README.md)
- [Z21 LAN spec](https://github.com/dcc-bigfred/proto/blob/main/docs/protos/z21.md)
- [pkg.go.dev](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/z21) — same protocol in Go

## Consumers

This crate is built for the [dcc-bigfred](https://github.com/dcc-bigfred) stack:

- **[LongFred](https://github.com/dcc-bigfred/longfred)** — wireless throttle firmware (`no_std`). Encodes and decodes Z21 / WiThrottle on the device; the firmware owns sockets.
- **[BigFred Wizard](https://github.com/dcc-bigfred/bigfred-wizard)** — event-tablet helper. Talks Z21 LAN when programming handsets on the layout.
- **[BigFred](https://github.com/dcc-bigfred/bigfred)** — layout hub (Go). Connected clients live in [`commandstation`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/commandstation).

## License

Apache-2.0
