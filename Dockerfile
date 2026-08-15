# syntax=docker/dockerfile:1

FROM golang:1.25-alpine@sha256:3eb6c2b3db8d55e38537302edb510b4417f8a115efbd5906d131ceba9468e29a AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/n0ding ./cmd/n0ding

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
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
