# ====================
# Stage 1: Build Backend
# ====================
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o songrequest

# ====================
# Stage 2: Runtime
# ====================
FROM alpine:edge

# Install yt-dlp dari Alpine repository
RUN apk add --no-cache \
    ca-certificates \
    ffmpeg \
    yt-dlp \
    && rm -rf /var/cache/apk/*

# Verify yt-dlp installed
RUN yt-dlp --version

WORKDIR /app

# === COPY BACKEND BINARY ===
COPY --from=builder /app/songrequest .
COPY --from=builder /app/overlay ./overlay

# === 🔥 COPY COOKIES FILE 🔥 ===
# Asumsi cookies.txt ada di folder backend (sejajar dengan go.mod)
COPY cookies.txt ./backend/cookies.txt

# Atau kalo cookies.txt ada di root project
# COPY cookies.txt ./cookies.txt

# Create directory for backend if needed
RUN mkdir -p /app/backend && mv /app/backend/cookies.txt /app/backend/ 2>/dev/null || true

EXPOSE 8080
CMD ["./songrequest"]