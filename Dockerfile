FROM alpine:latest

# Install semua dependencies
RUN apk add --no-cache \
    ca-certificates \
    python3 \
    py3-pip \
    ffmpeg \
    curl \
    && pip3 install --no-cache-dir yt-dlp \
    && ln -s /usr/bin/python3 /usr/bin/python

# Copy binary
WORKDIR /app
COPY songrequest .
COPY overlay ./overlay

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/api/queue || exit 1

EXPOSE 8080
CMD ["./songrequest"]