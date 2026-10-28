GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY   := keel
BINDIR   ?= $(HOME)/dev/bin
INSTALL  := $(BINDIR)/$(BINARY)
LDFLAGS  ?= -X main.buildVersion=$(VERSION)

.PHONY: build install config models run test fmt vet lint clean dist release

build:
	@mkdir -p bin
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/keel
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/kstat ./cmd/kstat

install: test build
	@mkdir -p $(BINDIR)
	ln -sf $(CURDIR)/bin/$(BINARY) $(INSTALL)
	ln -sf $(CURDIR)/bin/kstat $(BINDIR)/kstat
	@echo "Installed: $(INSTALL), $(BINDIR)/kstat"

config: ## Copy local/config to ~/.keel/config, for experimenting with settings (overwrites)
	@test -d local/config || { echo "no local/config; start with: cp -R examples/config local/"; exit 1; }
	@mkdir -p $(HOME)/.keel/config
	cp -R local/config/. $(HOME)/.keel/config/

models: build ## Write local/config/models.jsonc with keel models, and show what differs from ~/.keel/config; install with make config (usage: make models [MODELS="'anthropic:*' 'ollama:*'"])
	@mkdir -p local/config
	bin/$(BINARY) models $(MODELS) > local/config/models.jsonc.tmp
	mv local/config/models.jsonc.tmp local/config/models.jsonc
	@echo "Wrote local/config/models.jsonc; check it, then make config"
	@if [ -f $(HOME)/.keel/config/models.jsonc ]; then echo "Differences from ~/.keel/config/models.jsonc:"; bin/$(BINARY) models --diff $(MODELS); fi

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
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags '-s -w -X main.buildVersion=$(VERSION)' -o dist/arm64/$(BINARY) ./cmd/keel
	GOOS=darwin GOARCH=amd64 $(GO) build -ldflags '-s -w -X main.buildVersion=$(VERSION)' -o dist/amd64/$(BINARY) ./cmd/keel
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
