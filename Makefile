.PHONY: build test lint clean tag

GO ?= go
VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)

# Default target: build the plugin for the current platform.
build:
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -ldflags "$(LDFLAGS)" -o model-order.so .

test:
	$(GO) test -race -count=1 ./...

lint:
	@test -z "$$($(GO)fmt -l .)" || ($(GO)fmt -l . && exit 1)
	$(GO) vet ./...

clean:
	rm -f model-order.so model-order.h

# Tag a release from the VERSION file (usage: make tag).
tag:
	git tag -a v$(VERSION) -m "v$(VERSION)"
	git push origin v$(VERSION)
