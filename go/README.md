# dcc-bigfred/proto (Go)

<p align="center">
  <img src="logo.png" alt="bigfred-proto" width="200">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/dcc-bigfred/proto/go.svg)](https://pkg.go.dev/github.com/dcc-bigfred/proto/go)

Go libraries for talking to model-railroad command stations over **LocoNet**, **Z21 LAN**, and **WiThrottle**. Connected clients, loopback servers, and a LocoNet TCP gateway live in this module.

Requires Go ≥ 1.25.

## Install

```bash
go get github.com/dcc-bigfred/proto/go@v0.1.0
```

## Packages

| Import | Role |
|--------|------|
| [`pkgs/commandstation`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/commandstation) | `Station` clients: recommended `Open(uri)`; also `NewZ21Roco`, `NewWiThrottle`, `NewLocoNetSerial`, `NewLocoNetTCP`, `NewLocoNetTCPBinary` |
| [`pkgs/z21`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/z21) | Z21 LAN frames + UDP `Listen` server |
| [`pkgs/withrottle`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/withrottle) | WiThrottle lines + TCP client + `Listen` server |
| [`pkgs/loconet`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/loconet) | LocoNet framing + TCP gateway |
| [`pkgs/drive`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/drive) | `DriveHost` backend for the loopback servers |
| [`pkgs/telemetry`](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/telemetry) | Optional OpenTelemetry instruments |

## Quick start

```go
package main

import (
	"log"

	"github.com/dcc-bigfred/proto/go/pkgs/commandstation"
)

func main() {
	st, err := commandstation.Open("z21://192.168.0.111:21105")
	if err != nil {
		log.Fatal(err)
	}
	defer st.CleanUp()

	addr := commandstation.LocoAddr(3)
	if err := st.SetSpeed(addr, 50, true, 128); err != nil {
		log.Fatal(err)
	}
}
```

## Docs

- [Go client guide](https://github.com/dcc-bigfred/proto/blob/main/docs/go/README.md)
- [commandstation](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/commandstation) · [z21](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/z21) · [withrottle](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/withrottle) · [loconet](https://pkg.go.dev/github.com/dcc-bigfred/proto/go/pkgs/loconet) on pkg.go.dev
- [Z21 LAN spec](https://github.com/dcc-bigfred/proto/blob/main/docs/z21.md)
- [LocoNet spec](https://github.com/dcc-bigfred/proto/blob/main/docs/loconet.md)
- [WiThrottle spec](https://github.com/dcc-bigfred/proto/blob/main/docs/withrottle.md)

## Consumers

This module is built for the [dcc-bigfred](https://github.com/dcc-bigfred) stack:

- **[BigFred](https://github.com/dcc-bigfred/bigfred)** — layout hub (Go). Connected `Station` clients over Z21, LocoNet, and WiThrottle.
- **[BigFred Wizard](https://github.com/dcc-bigfred/bigfred-wizard)** — event-tablet helper (Rust). Talks Z21 LAN when programming handsets on the layout.
- **[LongFred](https://github.com/dcc-bigfred/longfred)** — wireless throttle firmware (Rust `no_std`). Encodes and decodes Z21 / WiThrottle on the device; the firmware owns sockets.

## License

Apache-2.0

## Versioning

Git tags for this module are `go/vX.Y.Z` (subdirectory prefix). Consumers still write `@vX.Y.Z`:

```bash
git tag go/v0.1.0 && git push origin go/v0.1.0
go get github.com/dcc-bigfred/proto/go@v0.1.0
```

See the [repository README](https://github.com/dcc-bigfred/proto#releasing) for crates.io tags (`vX.Y.Z`), which are separate.
