# proto

DCC-related protocol libraries: **LocoNet**, **Z21 LAN**, and **WiThrottle**.

Twin implementations in [Go](go/) (`github.com/dcc-bigfred/proto/go`) and
[Rust](rust/). Same wire semantics; shared [test vectors](testdata/) generated
from Go. Protocol specs live in [`docs/`](docs/). Library layering is in
[`ARCHITECTURE.md`](ARCHITECTURE.md).

This repository is the library. BigFred and LongFred will depend on it later;
they are not wired yet.

## Go

```bash
cd go
go test ./...
```

## Rust

Client crates (`z21`, `withrottle`) are `no_std` and allocation-free (LongFred /
ESP32-C6).

```bash
cd rust
cargo test
```

## License

Apache-2.0
