# Stage 1: Build
FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /aimux ./cmd/aimux

# Stage 2: Runtime
FROM alpine:3.21

RUN apk add --no-cache tmux
COPY --from=builder /aimux /usr/local/bin/aimux

# Non-root user for security
RUN adduser -D -h /home/aimux aimux
USER aimux
WORKDIR /home/aimux

ENTRYPOINT ["aimux"]
