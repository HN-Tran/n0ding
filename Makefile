.PHONY: build test check run

build:
	go build -o dist/n0ding ./cmd/n0ding

test:
	go test ./...

check:
	go test -race ./...
	go vet ./...

run:
	go run ./cmd/n0ding -config config/n0ding.local.toml
