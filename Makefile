GO ?= go

.PHONY: run build test vet fmt fmt-check scripts-check check

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

scripts-check:
	@for file in scripts/*.sh; do sh -n "$$file"; done
	@python3 -m py_compile test/sop/*.py

check: fmt-check vet test build scripts-check
