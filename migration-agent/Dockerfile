FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy the Go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY pkg ./pkg
COPY migration-agent ./migration-agent

# Build the migration-agent
WORKDIR /app/migration-agent
RUN CGO_ENABLED=0 GOOS=linux go build -o /migration-agent main.go

# Final image
FROM ubuntu:rolling

# Install necessary host-level tools for memory migration and network locking
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

# Copy the agent binary
COPY --from=builder /migration-agent /migration-agent

# Run the agent
ENTRYPOINT ["/migration-agent"]
