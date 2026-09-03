# dcc-bigfred/proto

Libraries for talking to model-railroad command stations over **LocoNet**, **Z21 LAN**, and **WiThrottle**. Use them when you build throttles, automation, or firmware that must drive locos, toggle functions, program CVs, or switch track power — without re-implementing wire formats.

Go provides connected **clients** and test **servers**. Rust provides **`no_std` protocol** crates for embedded targets (LongFred); the host owns sockets. Both languages share the same [golden test vectors](testdata/).

## Features

- **Unified drive API (Go)** — `SetSpeed`, `GetSpeed`, `SendFn`, `ListFunctions`, `EmergencyStop`, CV read/write, optional track power and LocoNet slot management
- **Three transports** — Z21 (UDP), LocoNet (serial / TCP), WiThrottle (TCP)
- **LAN autodetection (Go)** — scan a /24 for Z21, WiThrottle, and LocoNet-over-TCP
- **Protocol libraries** — frame encode/decode, checksums, WiThrottle line grammar; Rust crates are `no_std`, no `alloc`
- **Loopback servers (Go)** — Z21 UDP and WiThrottle TCP for tests and interop
- **LocoNet gateway (Go)** — fan-out upstream bus to binary/ASCII TCP listeners
- **Shared vectors** — `go run ./cmd/gen-vectors` writes `testdata/`; Go and Rust tests must match
- **CI interop** — Rust protocol crate ↔ Go `Listen` on every push
- **OpenTelemetry hooks (Go)** — optional driver metrics without OTel on the hot path

## Maturity

| Area | Go | Rust | Notes |
|------|:--:|:--:|-------|
| Z21 protocol | ✅ | ✅ | LAN frames, drive, functions, track power |
| Z21 `Station` client | ✅ | — | `NewZ21Roco` |
| Z21 server (`Listen`) | ✅ | 🧪 | Rust crate is experimental |
| WiThrottle protocol | ✅ | ✅ | Handshake, acquire, drive, fn, e-stop, track power |
| WiThrottle `Station` client | ✅ | — | JMRI, DCC-EX, LNWI, RB1110 |
| WiThrottle server | ✅ | 🧪 | Rust crate is experimental |
| LocoNet framing + gateway | ✅ | 🧪 | Rust gateway is a stub |
| LocoNet `Station` client | ✅ | — | Serial, LbServer ASCII, binary TCP |
| LocoNet slot lifecycle | ✅ | — | Acquire, release, dispatch, steal |
| CV programming | ✅ | — | Z21 + LocoNet; WiThrottle returns unsupported |
| Golden test vectors | ✅ | ✅ | Generated from Go |
| Usage guides | ✅ | ✅ | See [docs/](docs/) below |

✅ production-oriented in this repo · 🧪 experimental / stub · — not implemented

## Documentation

| Document | Audience |
|----------|----------|
| [Go client guide](docs/go/README.md) | Connect, drive, functions, e-stop, track power |
| [Rust protocol guide](docs/rust/README.md) | `no_std` Z21 / WiThrottle from firmware or `std::net` |
| [Z21 LAN spec](docs/z21.md) | Wire format reference |
| [LocoNet spec](docs/loconet.md) | Opcodes and framing |
| [WiThrottle spec](docs/withrottle.md) | Line protocol reference |
| [Architecture](ARCHITECTURE.md) | Repo layout, layers, equivalence |

## Quick start

**Go** — full client to a command station:

```bash
cd go && go test ./...
```

```bash
go get github.com/dcc-bigfred/proto/go/commandstation
```

**Rust** — protocol crate only (no sockets):

```bash
cd rust && cargo test --workspace --exclude dcc-bigfred-interop
```

```toml
dcc-bigfred-proto-z21 = "0.1"
dcc-bigfred-proto-withrottle = "0.1"
```

Path dependency: `dcc-bigfred-proto-z21`, `dcc-bigfred-proto-withrottle` under [`rust/`](rust/).

## Releasing (crates.io)

Bump `[workspace.package] version` in [`rust/Cargo.toml`](rust/Cargo.toml), commit, then tag and push `vX.Y.Z` (e.g. `v0.1.0`). The [release workflow](.github/workflows/release.yml) publishes every crate without `publish = false` and creates a GitHub Release. Set repository secret `CARGO_REGISTRY_TOKEN`.

## License

Apache-2.0
