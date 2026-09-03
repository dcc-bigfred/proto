.PHONY: test test-go test-rust test-interop

test: test-go test-rust

test-go:
	cd go && go test ./...

test-rust:
	cd rust && cargo test --workspace --exclude dcc-bigfred-interop

test-interop:
	mkdir -p rust/target
	cd go && go build -o ../rust/target/loopback-host ./cmd/loopback-host
	cd rust && PROTO_LOOPBACK_HOST=$(CURDIR)/rust/target/loopback-host cargo test -p dcc-bigfred-interop -- --test-threads=1
