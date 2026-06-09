.DEFAULT_GOAL := check
.PHONY: run clear tidy vendor test vet swag build check
include .env
export


clear:
	rm -f app* && rm -f main*

tidy:
	go mod tidy

vendor: tidy
	go mod vendor

run: clear tidy vendor
	go run cmd/server/*

build: clear tidy vendor
	go build -ldflags="-s -w" -buildvcs=false -o app cmd/server/*

vet:
	go vet ./...

test:
	go test ./...

check: clear tidy vendor vet test build
	rm -f app* && rm -f main*

swag:
	swag init -g cmd/server/*