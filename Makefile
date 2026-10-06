.PHONY: help run build test race vet fmt test-integration generate

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

generate:
	test "$$(buf --version)" = "1.72.0"
	test "$$(protoc-gen-go --version)" = "protoc-gen-go v1.36.12"
	test "$$(protoc-gen-go-grpc --version)" = "protoc-gen-go-grpc 1.6.2"
	buf generate api/proto