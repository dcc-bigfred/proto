.PHONY: test test-go test-rust test-interop vet thumb build-risc loopback-host gen-vectors

test: test-go test-rust

vet:
	$(MAKE) -C go vet

test-go:
	$(MAKE) -C go test

test-rust:
	$(MAKE) -C rust test

thumb:
	$(MAKE) -C rust thumb

build-risc:
	$(MAKE) -C rust build-risc

loopback-host:
	$(MAKE) -C go loopback-host

test-interop: loopback-host
	$(MAKE) -C rust test-interop PROTO_LOOPBACK_HOST=$(CURDIR)/rust/target/loopback-host

gen-vectors:
	$(MAKE) -C go gen-vectors
