MODULE  := github.com/zetic-ai/melange-cli
BINARY  := melange
BINDIR  := bin

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/build.Version=$(VERSION) \
	-X $(MODULE)/internal/build.Commit=$(COMMIT) \
	-X $(MODULE)/internal/build.Date=$(DATE)

# Codegen: vendored backend spec -> OpenAPI 3.0 -> Go client.
# openapi-down-convert is pinned here; oapi-codegen is pinned via the go.mod
# tool directive, so `make gen` is reproducible.
DOWN_CONVERT := npx --yes @apiture/openapi-down-convert@0.14.2

.PHONY: build test lint fmt gen gen-check

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) ./cmd/melange

test:
	go test ./...

lint:
	golangci-lint run

fmt:
	gofmt -l -w .

gen:
	$(DOWN_CONVERT) --input openapi/public-v1.json --output openapi/public-v1.3.0.json
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config .oapi-codegen.yml openapi/public-v1.3.0.json

gen-check:
	$(MAKE) gen
	git diff --exit-code -- openapi internal/api/gen
