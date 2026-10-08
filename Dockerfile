FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
# BuildKit cache mounts: the module cache and the Go build cache persist on
# the builder across image builds, so a source change recompiles only the
# changed packages instead of the whole tree per image (the compose stack
# builds this file 4× and Dockerfile.peel 5×). Both mounts default to
# shared mode, which Go's caches are safe under.
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o /bin/ ./cmd/zester-master ./cmd/zester-peel ./cmd/zester ./cmd/zester-watchdog \
    && CGO_ENABLED=0 GOOS=linux go build -o /bin/playground-init ./playground/init \
    && CGO_ENABLED=0 GOOS=linux go build -o /bin/zester-cli ./playground/zester-cli

FROM alpine:3.24

RUN apk add --no-cache ca-certificates curl bash git openssh-client

COPY --from=builder /bin/zester-master /usr/local/bin/zester-master
COPY --from=builder /bin/zester-peel /usr/local/bin/zester-peel
COPY --from=builder /bin/playground-init /usr/local/bin/playground-init
COPY --from=builder /bin/zester-cli /usr/local/bin/zester-cli
COPY --from=builder /bin/zester /usr/local/bin/zester
COPY --from=builder /bin/zester-watchdog /usr/local/bin/zester-watchdog

COPY playground/states /playground/states
COPY playground/settings /playground/settings
COPY playground/reactor /playground/reactor
COPY playground/auto-approve.sh /playground/auto-approve.sh

ENTRYPOINT []
