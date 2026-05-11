# 🧠 M3TAL GoBack (System Brain)

This repository contains the **Core Backend API and Intelligence Layer** for the M3TAL platform. It is responsible for orchestrating the "Sense-Think-Act" loop.

## 📦 Deployment

GoBack is a Go-native binary that runs inside a hardened Docker container.

### 1. Configuration (`.env`)
```ini
PORT=8080
DB_PATH=/mnt/config/m3tal.db
LOG_PATH=/mnt/config/logs/m3tal.log
STATE_DIR=/mnt/config/state
```

### 2. Infrastructure Requirements
The backend requires persistent storage mounted at `/mnt/config` to track system state and metrics history.

## 🏗️ Architecture

- **Registry**: Manages the discovery of all `*-compose.yml` stacks.
- **Monitor**: Continuous health checks of the Docker socket.
- **Metrics**: Aggregates Prometheus/Exporter data into unified JSON feeds.
- **Decision Engine**: Native Go implementation of the healing logic.

## 🛠 Development
```bash
# Verify Go version
go version # (Requires 1.22+)

# Run locally
go run main.go
```
