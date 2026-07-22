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

.PHONY: build test lint fmt gen gen-check docs docs-check fixtures-check

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
	go run ./tools/openapi30 --input openapi/public-v1.3.0.json --output openapi/public-v1.3.0.json
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config .oapi-codegen.yml openapi/public-v1.3.0.json

gen-check:
	$(MAKE) gen
	git diff --exit-code -- openapi internal/api/gen

docs:
	go run ./tools/gendocs docs/reference

docs-check:
	$(MAKE) docs
	git diff --exit-code -- docs/reference

# Local-dev sync check for the shared contract fixtures: re-copy the backend's
# committed fixtures and fail if the local copy drifted. The backend
# (../zetic_backend) is the generator (`make fixtures` there); CI does NOT run
# this copy step — CI just runs the Go tests (internal/contract) on the
# committed openapi/fixtures/, so a stale copy is caught by a failing test.
# See openapi/FIXTURES_SOURCE for the source commit.
BACKEND ?= ../zetic_backend
fixtures-check:
	@test -d "$(BACKEND)/openapi/fixtures" || { \
		echo "backend fixtures not found at $(BACKEND)/openapi/fixtures"; exit 1; }
	cp "$(BACKEND)"/openapi/fixtures/*.json openapi/fixtures/
	git diff --exit-code -- openapi/fixtures
