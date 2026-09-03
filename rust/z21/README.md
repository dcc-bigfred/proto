# dcc-bigfred-proto-z21

Z21 LAN **protocol** library: encode and decode UDP datagrams. `#![no_std]`, no `alloc`, no sockets. Firmware or a `std` host owns UDP and writes the `WireBuf` onto the wire.

## Install

```toml
[dependencies]
dcc-bigfred-proto-z21 = "0.1"
heapless = { version = "0.8", default-features = false }
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

Parse inbound datagrams with `on_bytes` (`Event::Serial`, `Event::LocoInfo`). Speed `0` is a normal stop; `1` is e-stop.

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
- [Z21 LAN spec](https://github.com/dcc-bigfred/proto/blob/main/docs/z21.md)

## License

Apache-2.0
