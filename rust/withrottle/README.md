# dcc-bigfred-proto-withrottle

<p align="center">
  <img src="logo.png" alt="bigfred-proto" width="200">
</p>

WiThrottle **protocol** library: encode and decode TCP lines (JMRI, DCC-EX, LNWI, RB1110). `#![no_std]`, no `alloc`, no sockets. Firmware or a `std` host owns TCP and writes the `WireBuf` onto the stream.

## Install

```toml
[dependencies]
dcc-bigfred-proto-withrottle = "0.1"
heapless = { version = "0.8", default-features = false }
```

## Usage

Acquire a locomotive before speed, functions, or e-stop.

```rust
use dcc_bigfred_proto_withrottle as wt;

let mut cli = wt::Client::new("my-app", "app-01");
let mut out = wt::WireBuf::new();
cli.on_connect(&mut out)?; // N / HU / *+

cli.encode(&wt::Command::Acquire { addr: 3 }, &mut out)?;
cli.encode(
    &wt::Command::SetDirection {
        addr: 3,
        forward: true,
    },
    &mut out,
)?;
cli.encode(
    &wt::Command::SetSpeed {
        addr: 3,
        speed: 50,
    },
    &mut out,
)?;
cli.encode(
    &wt::Command::SetFunction {
        addr: 3,
        func: 0,
        on: true,
    },
    &mut out,
)?;
cli.encode(&wt::Command::EmergencyStop { addr: 3 }, &mut out)?;
cli.encode(&wt::Command::TrackPower { on: true }, &mut out)?;
```

TCP sketch (`std`):

```rust
use dcc_bigfred_proto_withrottle as wt;
use std::io::Write;
use std::net::TcpStream;

let mut stream = TcpStream::connect("192.168.0.42:12090")?;
let mut cli = wt::Client::new("my-app", "app-01");
let mut hello = wt::WireBuf::new();
cli.on_connect(&mut hello).unwrap();
stream.write_all(&hello)?;
```

Handshake burst typically includes `Event::Protocol`, `Event::Heartbeat`, and `Event::TrackPower`.

## Docs

- [Rust protocol guide](https://github.com/dcc-bigfred/proto/blob/main/docs/rust/README.md)
- [WiThrottle spec](https://github.com/dcc-bigfred/proto/blob/main/docs/protos/withrottle.md)
- [pkg.go.dev](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/withrottle) — same protocol in Go

## Consumers

This crate is built for the [dcc-bigfred](https://github.com/dcc-bigfred) stack:

- **[LongFred](https://github.com/dcc-bigfred/longfred)** — wireless throttle firmware (`no_std`). Encodes and decodes Z21 / WiThrottle on the device; the firmware owns sockets.
- **[BigFred Wizard](https://github.com/dcc-bigfred/bigfred-wizard)** — event-tablet helper. Talks Z21 LAN when programming handsets on the layout.
- **[BigFred](https://github.com/dcc-bigfred/bigfred)** — layout hub (Go). Connected clients live in [`commandstation`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/commandstation).

## License

Apache-2.0
