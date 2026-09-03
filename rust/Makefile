.PHONY: test thumb build-risc test-interop

PROTO_CRATES := -p dcc-bigfred-proto-z21 -p dcc-bigfred-proto-withrottle
THUMB_TARGET := thumbv7em-none-eabihf
RISC_TARGET := riscv32imac-unknown-none-elf

PROTO_LOOPBACK_HOST ?= $(CURDIR)/target/loopback-host

test:
	cargo nextest run --workspace --exclude dcc-bigfred-interop --profile ci

thumb:
	cargo build $(PROTO_CRATES) --target $(THUMB_TARGET)

build-risc:
	cargo build $(PROTO_CRATES) --target $(RISC_TARGET)

test-interop:
	PROTO_LOOPBACK_HOST="$(PROTO_LOOPBACK_HOST)" cargo nextest run -p dcc-bigfred-interop --profile interop --test-threads=1
