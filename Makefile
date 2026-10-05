.PHONY: help run build test race vet fmt test-integration

help:
	@printf '%s\n' 'make run               Run with local .env' 'make build             Build this service' 'make test / race / vet Local checks' 'make test-integration  Test with a prepared isolated database'

run:
	@set -ea; . ./.env; set +a; exec go run ./cmd/orders

build:
	go build -o bin/orders ./cmd/orders

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

test-integration:
	go test -tags=integration -race -count=1 -run '^TestOrdersRepositoryPostgres$$' -timeout 60s -v ./internal/repository
