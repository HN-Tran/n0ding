.PHONY: build test check run docker-test docker-check docker-shell

GO_IMAGE ?= golang:1.25
DOCKER_GO = docker run --rm \
	--volume "$(CURDIR):/src" \
	--workdir /src \
	--user "$$(id -u):$$(id -g)" \
	--env GOCACHE=/src/.tmp/go-build-cache \
	--env GOMODCACHE=/src/.tmp/go-mod-cache \
	--env GOTMPDIR=/src/.tmp/go-tmp \
	$(GO_IMAGE)

build:
	go build -o dist/n0ding ./cmd/n0ding

test:
	go test ./...

check:
	go test -race ./...
	go vet ./...

run:
	go run ./cmd/n0ding -config config/n0ding.local.toml

docker-test:
	$(DOCKER_GO) sh -c 'mkdir -p "$$GOCACHE" "$$GOMODCACHE" "$$GOTMPDIR" && go test ./...'

docker-check:
	$(DOCKER_GO) sh -c 'mkdir -p "$$GOCACHE" "$$GOMODCACHE" "$$GOTMPDIR" && go test -race ./... && go vet ./...'

docker-shell:
	$(DOCKER_GO) sh
