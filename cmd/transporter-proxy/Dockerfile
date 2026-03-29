FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/transporter-proxy/main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -o /transporter-proxy .

FROM alpine:latest
RUN apk --no-cache add ca-certificates iptables iproute2
COPY --from=builder /transporter-proxy /transporter-proxy
ENTRYPOINT ["/transporter-proxy"]
