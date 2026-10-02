.PHONY: build test test-panel lint clean tag

GO ?= go
NODE ?= node
VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)

# Default target: build the plugin for the current platform.
build:
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -ldflags "$(LDFLAGS)" -o model-registry.so .

test:
	$(GO) test -race -count=1 ./...

# The Go tests render the panel but never run it, so the alias editor's behaviour
# needs a real browser. PW_DIR must point at a node_modules tree holding
# playwright; the target is not part of `test` because that dependency is not
# vendored. The script exits 77 when playwright is missing.
test-panel:
	$(NODE) scripts/alias-editor-test.js

lint:
	@test -z "$$($(GO)fmt -l .)" || ($(GO)fmt -l . && exit 1)
	$(GO) vet ./...

clean:
	rm -f model-registry.so model-registry.h

# Tag a release from the VERSION file (usage: make tag).
tag:
	git tag -a v$(VERSION) -m "v$(VERSION)"
	git push origin v$(VERSION)
