# OneClub developer commands (Technical Doc §5.1).
SHELL := bash
GO ?= go
ADMIN_URL ?= postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
INSTANCE ?= mgcc
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X oneclub/internal/app.Version=$(VERSION)

.PHONY: build test unit e2e lint openapi api worker migrate provision-dev seed-demo web web-dev docker clean

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/oneclub ./cmd/oneclub
	$(GO) build -trimpath -ldflags "-s -w" -o bin/bridge-agent ./cmd/bridge-agent

unit:
	$(GO) test -count=1 ./internal/...

e2e:
	ONECLUB_TEST_ADMIN_URL=$(ADMIN_URL) $(GO) test -count=1 -timeout 20m ./test/e2e/...

test: unit e2e

lint:
	golangci-lint run ./...

openapi:
	$(GO) run ./cmd/oneclub openapi -o api/openapi/openapi.json
	cd web && pnpm --filter @oneclub/api-client generate

# Local development instance (see docs/runbooks/local-development.md)
provision-dev: build
	./bin/oneclub instance create -admin-url "$(ADMIN_URL)" -code $(INSTANCE) \
	  -name "Modern Golf & Country Club" -super-admin-email admin@moderngolf.id \
	  -platform-admin-email platform@oneclub.id -public-url http://localhost:5173

seed-demo: build
	set -a; source .env.local; set +a; ./bin/oneclub seed-demo

api: build
	set -a; source .env.local; set +a; ./bin/oneclub api

worker: build
	set -a; source .env.local; set +a; ./bin/oneclub worker

migrate: build
	set -a; source .env.local; set +a; ./bin/oneclub migrate up

web:
	cd web && pnpm install && pnpm -r build

web-dev:
	cd web && pnpm dev

docker:
	docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(VERSION) -t oneclub:$(VERSION) .
	docker build -f deploy/docker/Dockerfile.web --target static -t oneclub-static:$(VERSION) .
	docker build -f deploy/docker/Dockerfile.web --target web -t oneclub-web:$(VERSION) .

clean:
	rm -rf bin var
