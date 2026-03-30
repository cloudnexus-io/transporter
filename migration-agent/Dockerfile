FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg ./pkg
COPY migration-agent ./migration-agent

WORKDIR /app/migration-agent
RUN CGO_ENABLED=0 GOOS=linux go build -o /migration-agent main.go

FROM ubuntu:rolling

RUN apt-get update && apt-get install -y \
    ca-certificates \
    tar \
    findutils \
    util-linux \
    criu \
    iproute2 \
    iptables \
    nftables \
    libbsd0 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /migration-agent /migration-agent

ENTRYPOINT ["/migration-agent"]
