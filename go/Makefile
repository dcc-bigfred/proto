.PHONY: vet test loopback-host gen-vectors

# JUnit path for CI; local runs still get a report next to the module.
JUNITFILE ?= go-junit.xml

# Binary consumed by rust interop tests.
LOOPBACK_HOST ?= ../rust/target/loopback-host

vet:
	go vet ./...

test:
	go run gotest.tools/gotestsum@latest --junitfile "$(JUNITFILE)" --format pkgname -- -count=1 -race ./...

loopback-host:
	mkdir -p "$(dir $(LOOPBACK_HOST))"
	go build -o "$(LOOPBACK_HOST)" ./cmd/loopback-host

gen-vectors:
	go run ./cmd/gen-vectors
