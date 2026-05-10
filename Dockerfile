# --- Build Stage ---
FROM golang:1.24-alpine AS builder

# NO NETWORK REQUIRED: Using local vendor folder
WORKDIR /app

# Copy all source and dependencies
COPY . .

# Build for Linux using the vendor folder
# -mod=vendor tells Go to use the local vendor directory instead of the network
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -o m3tal-backend .

# Use a stable alpine version and enable community repo for hardware tools
FROM alpine:3.20

RUN echo "https://dl-cdn.alpinelinux.org/alpine/v3.20/community" >> /etc/apk/repositories && \
    apk update && \
    apk add --no-cache ca-certificates radeontop smartmontools pciutils || true

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/m3tal-backend .

# Set environment defaults
ENV STATE_DIR=/docker/control-plane/state
ENV TZ=America/Denver

# OCI Image Labels
LABEL org.opencontainers.image.title="M3TAL Control Plane" \
      org.opencontainers.image.description="AI-Powered Docker Control Plane Backend" \
      org.opencontainers.image.vendor="M3TAL" \
      org.opencontainers.image.logo="https://raw.githubusercontent.com/jakej985-rgb/M3tal-Media-Server/main/docs/logo.svg"

# Run the backend
CMD ["./m3tal-backend"]
