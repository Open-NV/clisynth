.PHONY: build test test-race vet fmt check run docker-build

build:
	go build -o bin/clisynth ./cmd/clisynth

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

check: fmt vet test

run:
	go run ./cmd/clisynth serve --definitions ./examples/definitions

docker-build:
	docker build -t clisynth:dev .
