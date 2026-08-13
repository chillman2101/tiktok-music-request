# ====================
# Stage 1: Build Backend
# ====================
FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o songrequest

# ====================
# Stage 2: Runtime
# ====================
FROM alpine:latest

# Install dependencies (tanpa pip)
RUN apk add --no-cache \
    ca-certificates \
    ffmpeg \
    python3 \
    wget \
    && rm -rf /var/cache/apk/*

# Install yt-dlp via binary (bukan pip!)
RUN wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/local/bin/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp

WORKDIR /app
COPY --from=builder /app/songrequest .
COPY --from=builder /app/overlay ./overlay

EXPOSE 8080
CMD ["./songrequest"]