.PHONY: test test-go test-rust

test: test-go test-rust

test-go:
	cd go && go test ./...

test-rust:
	cd rust && cargo test
