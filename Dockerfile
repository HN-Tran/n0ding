# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/n0ding ./cmd/n0ding

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 65532 n0ding && \
    adduser -S -D -H -u 65532 -G n0ding n0ding && \
    mkdir -p /data /etc/n0ding && \
    chown -R n0ding:n0ding /data
COPY --from=build /out/n0ding /usr/local/bin/n0ding
COPY config/n0ding.toml /etc/n0ding/n0ding.toml
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/n0ding"]
CMD ["-config", "/etc/n0ding/n0ding.toml"]
