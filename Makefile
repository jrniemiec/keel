GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY   := keel
BINDIR   ?= $(HOME)/dev/bin
INSTALL  := $(BINDIR)/$(BINARY)
LDFLAGS  ?= -X main.version=$(VERSION)

.PHONY: build install run test fmt vet lint clean dist release

build:
	@mkdir -p bin
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/keel

install: test build
	@mkdir -p $(BINDIR)
	ln -sf $(CURDIR)/bin/$(BINARY) $(INSTALL)
	@echo "Installed: $(INSTALL)"

run: build
	bin/$(BINARY)

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

dist: ## Build release tarballs for darwin arm64+amd64 (usage: make dist VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then echo "usage: make dist VERSION=x.y.z"; exit 1; fi
	@mkdir -p dist/arm64 dist/amd64
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags '-s -w -X main.version=$(VERSION)' -o dist/arm64/$(BINARY) ./cmd/keel
	GOOS=darwin GOARCH=amd64 $(GO) build -ldflags '-s -w -X main.version=$(VERSION)' -o dist/amd64/$(BINARY) ./cmd/keel
	tar -czf dist/$(BINARY)_$(VERSION)_darwin_arm64.tar.gz -C dist/arm64 $(BINARY)
	tar -czf dist/$(BINARY)_$(VERSION)_darwin_amd64.tar.gz -C dist/amd64 $(BINARY)
	@rm -rf dist/arm64 dist/amd64
	@echo "    OK: dist/$(BINARY)_$(VERSION)_darwin_arm64.tar.gz"
	@echo "    OK: dist/$(BINARY)_$(VERSION)_darwin_amd64.tar.gz"

release: ## Tag and push a release (usage: make release VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then echo "usage: make release VERSION=x.y.z"; exit 1; fi
	git tag v$(VERSION)
	git push origin v$(VERSION)

clean:
	rm -rf bin/ dist/
