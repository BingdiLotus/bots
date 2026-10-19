GO ?= go

.PHONY: run build test vet fmt fmt-check check

run:
	$(GO) run ./cmd/bots

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/bots ./cmd/bots

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal

fmt-check:
	@files="$$(gofmt -l cmd internal)"; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

check: fmt-check vet test build
