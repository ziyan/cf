.PHONY: build clean test lint format vendor tidy

BINARY := cf
GO := go
GOFLAGS := -mod=vendor
CGO_ENABLED := 0

VERSION ?= $(shell git describe --tags 2>/dev/null || echo 0.1.0)
COMMIT ?= $(shell git describe --match=NeVeRmAtCh --always --abbrev=40 --dirty)
LDFLAGS := -s -w \
	-X github.com/ziyan/cf/internal/version.version=$(VERSION) \
	-X github.com/ziyan/cf/internal/version.commit=$(COMMIT)

build:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) ./command/

clean:
	rm -f $(BINARY)

test:
	$(GO) test $(GOFLAGS) ./... -count=1

lint:
	golangci-lint run ./...

format:
	gofmt -s -w .

vendor:
	$(GO) mod tidy
	$(GO) mod vendor

tidy:
	$(GO) mod tidy
