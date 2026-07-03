FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/zester-master ./cmd/zester-master
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/zester-peel ./cmd/zester-peel
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/playground-init ./playground/init
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/zester-cli ./playground/zester-cli
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/zester ./cmd/zester
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/zester-watchdog ./cmd/zester-watchdog

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
COPY playground/auto-approve.sh /playground/auto-approve.sh

ENTRYPOINT []
