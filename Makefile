.PHONY: build test lint

build:
	CGO_ENABLED=0 go build -o bin/inscribe ./cmd/inscribe

test:
	go test -race ./...

lint:
	golangci-lint run
