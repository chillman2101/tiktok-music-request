# ====================
# Stage 1: Build Backend
# ====================
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o songrequest

# ====================
# Stage 2: Runtime
# ====================
FROM alpine:latest

# Install yt-dlp dan dependencies
RUN apk add --no-cache \
    ca-certificates \
    python3 \
    py3-pip \
    ffmpeg \
    && pip3 install --no-cache-dir yt-dlp \
    && ln -s /usr/bin/python3 /usr/bin/python \
    && rm -rf /var/cache/apk/*

WORKDIR /app
COPY --from=builder /app/songrequest .
COPY --from=builder /app/overlay ./overlay

EXPOSE 8080
CMD ["./songrequest"]