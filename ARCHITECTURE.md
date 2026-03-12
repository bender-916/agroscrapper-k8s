# Architecture Document: SQLite to PostgreSQL Migration & Kubernetes Deployment

## Executive Summary

This document outlines the migration strategy for converting the agroscrapper-bot from SQLite (embedded) to PostgreSQL (client/server) and preparing it for Kubernetes deployment. The migration enables horizontal scaling, persistent data management, and production-grade reliability.

---

## 1. Current Architecture Analysis

### 1.1 State Assessment
```
Current Stack:
- Database: SQLite (github.com/mattn/go-sqlite3) - CGO required
- Build: CGO_ENABLED=0 with static binary
- Image: scratch (distroless) - no libc
- Storage: Local file system (cursos.db)
- Execution: Single instance only
- Configuration: CLI flags + environment variables
```

### 1.2 Limitations
- **Single-node only**: SQLite file locks prevent multiple instances
- **Data loss risk**: Container restart = data loss in scratch image
- **No horizontal scaling**: Cannot run multiple replicas
- **CGO conflict**: Current static build incompatible with pgx (needs libc)

---

## 2. Target Architecture

### 2.1 Proposed Stack
```
Database Layer:
- PostgreSQL 16+ (lightweight, suitable for small workloads)
- Driver: github.com/jackc/pgx/v5 (pure Go, preferred over lib/pq)
- Migration Tool: golang-migrate/migrate or pressly/goose
- Connection Pooling: pgx built-in pool

Application Layer:
- Base Image: distroless/cc (includes glibc for pgx)
- ORM/Builder: jmoiron/sqlx (optional) or raw pgx
- Graceful Shutdown: context cancellation + signal handling

Infrastructure Layer (Kubernetes):
- Deployment: Single replica (sufficient for scraping workload)
- Database: Bitnami PostgreSQL Helm chart or CloudNativePG
- PVC: PostgreSQL data persistence
- ConfigMap: Application configuration (non-sensitive)
- Secrets: Database credentials + Telegram tokens
- CronJob: Trigger scraping on schedule (optional)
```

---

## 3. Database Migration Strategy

### 3.1 Schema Translation

**Current SQLite Schema:**
```sql
CREATE TABLE IF NOT EXISTS cursos (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT UNIQUE,
    titulo TEXT,
    lugar TEXT,
    periodo TEXT,
    hora TEXT,
    plazas TEXT,
    costo TEXT
);
```

**PostgreSQL Equivalent:**
```sql
CREATE TABLE IF NOT EXISTS cursos (
    id SERIAL PRIMARY KEY,
    url VARCHAR(2048) UNIQUE NOT NULL,
    titulo VARCHAR(500),
    lugar VARCHAR(255),
    periodo VARCHAR(255),
    hora VARCHAR(100),
    plazas VARCHAR(50),
    costo VARCHAR(100),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Index for URL lookups
CREATE INDEX idx_cursos_url ON cursos(url);
```

### 3.2 Migration Strategy Options

**Recommended: Online Migration with Downtime (simpler for this use case)**

Since data is scraped and not user-generated, acceptable to:
1. Export existing SQLite data (if any needs preservation)
2. Deploy PostgreSQL with fresh schema
3. Run new version of app

**Migration Tool Selection:**
- **Primary Choice: golang-migrate/migrate** (v4.18.0+)
  - Industry standard
  - CLI + Go library
  - Transactional migrations
  - Version locking
- **Alternative: pressly/goose**
  - Pure Go
  - No external dependencies
  - Simpler for small projects

### 3.3 Migration Files Structure
```
migrations/
├── 000001_create_cursos_table.down.sql
├── 000001_create_cursos_table.up.sql
├── 000002_add_indexes.down.sql
└── 000002_add_indexes.up.sql
```

**Sample Migration (up):**
```sql
-- 000001_create_cursos_table.up.sql
CREATE TABLE IF NOT EXISTS cursos (
    id SERIAL PRIMARY KEY,
    url VARCHAR(2048) UNIQUE NOT NULL,
    titulo VARCHAR(500),
    lugar VARCHAR(255),
    periodo VARCHAR(255),
    hora VARCHAR(100),
    plazas VARCHAR(50),
    costo VARCHAR(100),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_cursos_url ON cursos(url);
```

---

## 4. Code Changes Required

### 4.1 Dependencies (go.mod)

**Remove:**
```
github.com/mattn/go-sqlite3 v1.14.24
```

**Add:**
```
github.com/jackc/pgx/v5 v5.7.1
github.com/jackc/pgx/v5/pgxpool v5.7.1
github.com/golang-migrate/migrate/v4 v4.18.1 // for migrations
```

### 4.2 Database Initialization Changes

**Current Code Pattern:**
```go
db, err := sql.Open("sqlite3", dbFile)
```

**New Pattern:**
```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    "context"
)

// Connection string from env vars
connString := os.Getenv("DATABASE_URL")
// or build: postgres://user:pass@host:5432/dbname?sslmode=require

config, err := pgxpool.ParseConfig(connString)
if err != nil {
    log.Fatal(err)
}

// Connection pool settings for small workload
config.MaxConns = 5
config.MinConns = 1
config.MaxConnLifetime = time.Hour
config.MaxConnIdleTime = 30 * time.Minute

pool, err := pgxpool.NewWithConfig(context.Background(), config)
if err != nil {
    log.Fatal(err)
}
defer pool.Close()

// Verify connection
if err := pool.Ping(context.Background()); err != nil {
    log.Fatal(err)
}
```

### 4.3 SQL Query Migrations

| SQLite | PostgreSQL |
|--------|------------|
| `INTEGER PRIMARY KEY AUTOINCREMENT` | `SERIAL PRIMARY KEY` |
| `INSERT INTO ... VALUES (?, ?, ?)` | `INSERT INTO ... VALUES ($1, $2, $3)` |
| `url=?` | `url=$1` |
| `SELECT EXISTS(...)` | Same (standard SQL) |
| `TEXT` | `VARCHAR` or `TEXT` |

### 4.4 SQL Builder Pattern (Recommended)

Instead of positional arguments, use named parameters with sqlx or squirrel:

```go
// Option 1: Raw pgx (most changes)
_, err = pool.Exec(ctx, 
    "INSERT INTO cursos (url, titulo, lugar, periodo, hora, plazas, costo) VALUES ($1, $2, $3, $4, $5, $6, $7)",
    curso.URL, curso.Titulo, curso.Lugar, curso.Periodo, curso.Hora, curso.Plazas, curso.Costo,
)

// Option 2: Using squirrel (code generation-like query builder)
import "github.com/Masterminds/squirrel"

query := squirrel.Insert("cursos").
    Columns("url", "titulo", "lugar", "periodo", "hora", "plazas", "costo").
    Values(curso.URL, curso.Titulo, curso.Lugar, curso.Periodo, curso.Hora, curso.Plazas, curso.Costo).
    PlaceholderFormat(squirrel.Dollar) // PostgreSQL $

sql, args, err := query.ToSql()
```

### 4.5 Graceful Shutdown Implementation

```go
package main

import (
    "context"
    "os"
    "os/signal"
    "syscall"
    "time"
)

func setupGracefulShutdown(pool *pgxpool.Pool) context.Context {
    ctx, cancel := context.WithCancel(context.Background())
    
    c := make(chan os.Signal, 1)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    
    go func() {
        sig := <-c
        log.Printf("Received signal %v, shutting down gracefully...", sig)
        
        // Create shutdown context with timeout
        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutdownCancel()
        
        // Close database pool
        pool.Close()
        
        cancel() // Cancel main context
    }()
    
    return ctx
}

// Usage in main()
func main() {
    pool := initDB()
    ctx := setupGracefulShutdown(pool)
    
    // Pass ctx to all operations
    // ... rest of application
}
```

### 4.6 Environment Variables (Updated)

| Variable | Old | New | Required |
|----------|-----|-----|----------|
| `DB_FILE` | `cursos.db` | - | ❌ Remove |
| `DATABASE_URL` | - | `postgres://...` | ✅ Yes |
| `DB_HOST` | - | `postgres` | ⚪ Optional (if not using URL) |
| `DB_PORT` | - | `5432` | ⚪ Optional |
| `DB_NAME` | - | `agroscrapper` | ⚪ Optional |
| `DB_USER` | - | `postgres` | ⚪ Optional |
| `DB_PASSWORD` | - | `...` | ⚪ Optional |
| `TELEGRAM_TOKEN` | Same | Same | ✅ Yes |
| `TELEGRAM_CHATID` | Same | Same | ✅ Yes |
| `TELEGRAM_THREADID` | Same | Same | ⚪ Optional |

---

## 5. Containerfile Updates

### 5.1 Current vs New Comparison

**Current Containerfile (SQLite-compatible):**
```dockerfile
# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o agroscrapper .

# Runtime stage
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/agroscrapper /agroscrapper
ENTRYPOINT ["/agroscrapper"]
```

**New Containerfile (PostgreSQL-compatible):**
```dockerfile
# Build stage
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build with CGO enabled (pgx doesn't strictly need it, but some dependencies might)
# pgx/v5 is pure Go, so CGO_ENABLED=0 works!
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o agroscrapper .

# Runtime stage - use distroless/cc for glibc (pgx compatible)
FROM gcr.io/distroless/cc-debian12:latest

# Copy CA certificates and timezone data
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy binary
COPY --from=builder /app/agroscrapper /agroscrapper

# Run as non-root (distroless default is non-root)
USER nonroot:nonroot

ENTRYPOINT ["/agroscrapper"]
```

### 5.2 Key Changes

| Aspect | Before | After | Reason |
|--------|--------|-------|--------|
| Runtime base | `scratch` | `distroless/cc-debian12` | Minimal image with glibc for DNS/SSL |
| CGO | Disabled | Disabled | pgx/v5 is pure Go - CGO not needed |
| Timezone data | Missing | Included | Proper timestamp handling |
| User | Root (implicit) | nonroot | Security best practice |
| CA certificates | Copied manually | Same + tzdata | HTTPS requires valid CAs |

### 5.3 Alternative: Static Build (Still Possible)

```dockerfile
# If you want to keep scratch image
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/agroscrapper /agroscrapper
# Note: Must ensure pgx uses Go's net resolver, not CGO
ENV GODEBUG=netdns=go
ENTRYPOINT ["/agroscrapper"]
```

> **Note:** Using `distroless/cc` is recommended over `scratch` for better compatibility with Go's standard library networking features.

---

## 6. Kubernetes Manifests

### 6.1 File Structure
```
k8s/
├── 00-namespace.yaml          # Optional: dedicated namespace
├── 01-postgres-secret.yaml    # Database credentials
├── 02-postgres-pvc.yaml       # Persistent volume claim
├── 03-postgres-deployment.yaml # PostgreSQL deployment
├── 04-postgres-service.yaml   # PostgreSQL service
├── 05-configmap.yaml           # App configuration
├── 06-app-secret.yaml          # Telegram tokens
├── 07-deployment.yaml          # Application deployment
├── 08-service.yaml            # Application service (optional)
├── 09-cronjob.yaml             # Alternative: CronJob instead of Deployment
└── kustomization.yaml          # Kustomize config
```

### 6.2 PostgreSQL Deployment (Recommended: StatefulSet)

**Why StatefulSet over Deployment for PostgreSQL:**
- Stable network identity (pod-0, pod-1)
- Stable storage (PVC per pod)
- Ordered deployment
- Required if running cluster mode (not needed for single node)

**Option A: StatefulSet (Single Instance)**
```yaml
# 02-postgres-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: postgres-data
  namespace: agroscrapper
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi  # Lightweight workload
  storageClassName: standard  # Adjust per cluster

---
# 03-postgres-deployment.yaml (simpler than StatefulSet for single instance)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: agroscrapper
  labels:
    app: postgres
spec:
  replicas: 1  # Single instance only
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
        - name: postgres
          image: docker.io/bitnami/postgresql:16.4.0-debian-12-r20
          env:
            - name: POSTGRESQL_USERNAME
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: username
            - name: POSTGRESQL_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: password
            - name: POSTGRESQL_DATABASE
              value: "agroscrapper"
            - name: POSTGRESQL_SYNCHRONOUS_COMMIT_MODE
              value: "off"  # Performance for single-node
            - name: POSTGRESQL_FSYNC
              value: "off"  # Acceptable for non-critical scraper
          ports:
            - containerPort: 5432
              name: postgres
          volumeMounts:
            - name: postgres-data
              mountPath: /bitnami/postgresql
          resources:
            requests:
              memory: "128Mi"
              cpu: "100m"
            limits:
              memory: "512Mi"
              cpu: "500m"
          livenessProbe:
            exec:
              command:
                - /opt/bitnami/scripts/postgresql/healthcheck.sh
            initialDelaySeconds: 30
            periodSeconds: 10
          readinessProbe:
            exec:
              command:
                - /opt/bitnami/scripts/postgresql/healthcheck.sh
            initialDelaySeconds: 5
            periodSeconds: 5
      volumes:
        - name: postgres-data
          persistentVolumeClaim:
            claimName: postgres-data

---
# 04-postgres-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: agroscrapper
spec:
  type: ClusterIP
  ports:
    - port: 5432
      targetPort: 5432
  selector:
    app: postgres
```

### 6.3 Secrets

```yaml
# 01-postgres-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: postgres-secret
  namespace: agroscrapper
type: Opaque
stringData:
  username: "agroscrapper_user"
  password: "changeme_in_production"  # Use sealed-secrets or external-secrets-operator in prod

---
# 06-app-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: app-secret
  namespace: agroscrapper
type: Opaque
stringData:
  telegram-token: "your_bot_token_here"
  telegram-chatid: "your_chat_id_here"
  telegram-threadid: "your_thread_id_here"  # Optional
```

### 6.4 Application Configuration (ConfigMap)

```yaml
# 05-configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: agroscrapper-config
  namespace: agroscroller
data:
  # Log verbosity
  LOG_LEVEL: "info"
  # Connection pool settings
  DB_MAX_CONNECTIONS: "5"
  DB_MIN_CONNECTIONS: "1"
  DB_MAX_CONN_LIFETIME: "1h"
  # Scraper settings
  SCRAPER_DELAY: "1s"
  SCRAPER_PARALLELISM: "3"
  # Target website
  TARGET_URL: "https://formacionagraria.tenerife.es/"
```

### 6.5 Application Deployment

```yaml
# 07-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: agroscrapper
  namespace: agroscrapper
  labels:
    app: agroscrapper
spec:
  replicas: 1  # Single instance (writes to DB, not scalable without coordination)
  selector:
    matchLabels:
      app: agroscrapper
  template:
    metadata:
      labels:
        app: agroscrapper
    spec:
      restartPolicy: Always  # Changed to Always for continuous operation
      containers:
        - name: agroscrapper
          image: ghcr.io/daklon/agroscrapper-bot:latest
          imagePullPolicy: IfNotPresent
          env:
            # Database connection
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: database-url
            # Telegram configuration
            - name: TELEGRAM_TOKEN
              valueFrom:
                secretKeyRef:
                  name: app-secret
                  key: telegram-token
            - name: TELEGRAM_CHATID
              valueFrom:
                secretKeyRef:
                  name: app-secret
                  key: telegram-chatid
            - name: TELEGRAM_THREADID
              valueFrom:
                secretKeyRef:
                  name: app-secret
                  key: telegram-threadid
            # From ConfigMap
            - name: LOG_LEVEL
              valueFrom:
                configMapKeyRef:
                  name: agroscrapper-config
                  key: LOG_LEVEL
            - name: DB_MAX_CONNECTIONS
              valueFrom:
                configMapKeyRef:
                  name: agroscrapper-config
                  key: DB_MAX_CONNECTIONS
          resources:
            requests:
              memory: "64Mi"
              cpu: "50m"
            limits:
              memory: "256Mi"
              cpu: "200m"
          # Consider adding liveness/readiness probes if http endpoint added
          # livenessProbe:
          #   exec:
          #     command:
          #       - /agroscrapper
          #       - --health-check
```

### 6.6 Alternative: CronJob (Recommended for Scraper)

Since this is a scraper that runs once and exits, a **CronJob** is more appropriate than a Deployment:

```yaml
# 09-cronjob.yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: agroscrapper
  namespace: agroscrapper
spec:
  schedule: "0 9 * * *"  # 9 AM daily (adjust to user's timezone)
  # schedule: "*/30 * * * *"  # Every 30 minutes (more frequent)
  timeZone: "Atlantic/Canary"  # User's timezone
  concurrencyPolicy: Forbid  # Don't run overlapping jobs
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      activeDeadlineSeconds: 600  # 10 minute timeout
      backoffLimit: 3
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: agroscrapper
              image: ghcr.io/daklon/agroscrapper-bot:latest
              imagePullPolicy: IfNotPresent
              env:
                - name: DATABASE_URL
                  valueFrom:
                    secretKeyRef:
                      name: postgres-secret
                      key: database-url
                - name: TELEGRAM_TOKEN
                  valueFrom:
                    secretKeyRef:
                      name: app-secret
                      key: telegram-token
                - name: TELEGRAM_CHATID
                  valueFrom:
                    secretKeyRef:
                      name: app-secret
                      key: telegram-chatid
                - name: TELEGRAM_THREADID
                  valueFrom:
                    secretKeyRef:
                      name: app-secret
                      key: telegram-threadid
              resources:
                requests:
                  memory: "64Mi"
                  cpu: "50m"
                limits:
                  memory: "256Mi"
                  cpu: "500m"
```

**Recommendation:** Use CronJob over Deployment for this scraper.

### 6.7 Kustomization (Recommended for Management)

```yaml
# kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: agroscrapper

resources:
  - 00-namespace.yaml
  - 01-postgres-secret.yaml
  - 02-postgres-pvc.yaml
  - 03-postgres-deployment.yaml
  - 04-postgres-service.yaml
  - 05-configmap.yaml
  - 06-app-secret.yaml
  - 09-cronjob.yaml  # or 07-deployment.yaml

configMapGenerator:
  - name: agroscrapper-config
    behavior: merge
    literals:
      - LOG_LEVEL=info

secretGenerator:
  - name: postgres-secret
    behavior: merge
    literals:
      - username=agroscrapper_user
      - password=REPLACE_ME_IN_PROD
      - database-url=postgres://agroscrapper_user:REPLACE_ME_IN_PROD@postgres:5432/agroscrapper?sslmode=disable

images:
  - name: ghcr.io/daklon/agroscrapper-bot
    newTag: v1.0.0
```

---

## 7. Local Testing Without Full K8s Cluster

### 7.1 Option A: Docker Compose (Recommended)

Create `docker-compose.yml` for local development:

```yaml
version: '3.8'

services:
  postgres:
    image: docker.io/bitnami/postgresql:16.4.0-debian-12-r20
    environment:
      POSTGRESQL_USERNAME: agroscrapper
      POSTGRESQL_PASSWORD: localdev
      POSTGRESQL_DATABASE: agroscrapper
    volumes:
      - postgres_data:/bitnami/postgresql
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "agroscrapper"]
      interval: 5s
      timeout: 5s
      retries: 5

  agroscrapper:
    build:
      context: .
      dockerfile: Containerfile
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      TELEGRAM_TOKEN: ${TELEGRAM_TOKEN}
      TELEGRAM_CHATID: ${TELEGRAM_CHATID}
      TELEGRAM_THREADID: ${TELEGRAM_THREADID}
      DATABASE_URL: postgres://agroscrapper:localdev@postgres:5432/agroscrapper?sslmode=disable
      LOG_LEVEL: debug
    command: ["/agroscrapper"]
    # Or run migrations first:
    # command: ["sh", "-c", "/usr/local/bin/migrate up && /agroscrapper"]

volumes:
  postgres_data:
```

**Usage:**
```bash
# Set environment variables
export TELEGRAM_TOKEN="your_token"
export TELEGRAM_CHATID="your_chatid"

# Run everything
docker-compose up --build

# Run migrations
docker-compose run --rm agroscrapper migrate -path migrations -database "postgres://agroscrapper:localdev@postgres:5432/agroscrapper?sslmode=disable" up

# Check database
docker-compose exec postgres psql -U agroscrapper -d agroscrapper
```

### 7.2 Option B: Kind (Kubernetes in Docker)

For testing actual Kubernetes manifests:

```bash
# Install kind
brew install kind  # macOS
# or
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.24.0/kind-linux-amd64

# Create cluster
kind create cluster --name agroscrapper-test

# Load local image into kind
kind load docker-image agroscrapper-bot:latest --name agroscrapper-test

# Apply manifests
kubectl apply -k k8s/

# Check status
kubectl get all -n agroscrapper
kubectl logs -n agroscrapper -l app=agroscrapper
```

### 7.3 Option C: K3d / K3s (Lightweight K8s)

```bash
# Install k3d
curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash

# Create cluster
k3d cluster create agroscrapper

# Deploy
kubectl apply -k k8s/
```

### 7.4 Migration Testing Script

```bash
#!/bin/bash
# test-migrations.sh

set -e

echo "Starting PostgreSQL..."
docker run -d \
  --name test-postgres \
  -e POSTGRES_USER=test \
  -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=agroscrapper \
  -p 5433:5432 \
  bitnami/postgresql:16

# Wait for PostgreSQL
echo "Waiting for PostgreSQL..."
until docker exec test-postgres pg_isready -U test; do
  sleep 1
done

# Run migrations
echo "Running migrations..."
migrate -path migrations -database "postgres://test:test@localhost:5433/agroscrapper?sslmode=disable" up

# Verify schema
echo "Verifying schema..."
docker exec test-postgres psql -U test -d agroscrapper -c "\dt"

# Tear down
echo "Cleaning up..."
docker stop test-postgres && docker rm test-postgres

echo "Migration test passed!"
```

---

## 8. Connection Pooling & Performance

### 8.1 Recommended pgxpool Configuration

```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    "time"
)

func createPool(connString string) (*pgxpool.Pool, error) {
    config, err := pgxpool.ParseConfig(connString)
    if err != nil {
        return nil, err
    }
    
    // Connection pool settings for lightweight workload
    config.MaxConns = 5                    // Maximum connections in pool
    config.MinConns = 1                    // Minimum connections to maintain
    config.MaxConnLifetime = time.Hour     // Recycle connections after 1 hour
    config.MaxConnIdleTime = 30 * time.Minute  // Close idle connections after 30 min
    config.HealthCheckPeriod = 5 * time.Minute // Health check interval
    
    // Connection timeout settings
    config.ConnConfig.ConnectTimeout = 10 * time.Second
    
    // Create pool
    pool, err := pgxpool.NewWithConfig(context.Background(), config)
    if err != nil {
        return nil, err
    }
    
    return pool, nil
}
```

### 8.2 Connection Pool Sizing

**For this scraper workload (single instance):**
- **MaxConns: 5** - Sufficient for scrape + operations
- **MinConns: 1** - Keep one connection warm
- **MaxConnLifetime: 1h** - Prevent stale/idle connections
- **MaxConnIdleTime: 30m** - Reclaim resources if idle

**Rationale:**
- Scraper is not highly concurrent (parallelism=3 in current code)
- Most DB operations are INSERT/SELECT by URL
- PostgreSQL on small Kubernetes resources (128MB-512MB)

### 8.3 Graceful Shutdown Integration

```go
func main() {
    // Create context for graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    // Setup signal handling
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    // Initialize database
    pool, err := createPool(os.Getenv("DATABASE_URL"))
    if err != nil {
        log.Fatal(err)
    }
    
    // Run application in goroutine
    errChan := make(chan error, 1)
    go func() {
        errChan <- runScraper(ctx, pool)
    }()
    
    // Wait for signal or completion
    select {
    case sig := <-sigChan:
        log.Printf("Received signal %v, shutting down...", sig)
        cancel() // Signal scraper to stop
        
        // Wait for completion with timeout
        select {
        case <-errChan:
        case <-time.After(10 * time.Second):
            log.Println("Shutdown timeout, forcing exit")
        }
        
    case err := <-errChan:
        if err != nil {
            log.Printf("Scraper error: %v", err)
        }
    }
    
    // Close connection pool
    pool.Close()
    log.Println("Shutdown complete")
}
```

---

## 9. Implementation Checklist

### Phase 1: Database Layer
- [ ] Add pgx/v5 dependency to go.mod
- [ ] Remove go-sqlite3 dependency
- [ ] Create migrations folder
- [ ] Write 000001_create_cursos_table migration
- [ ] Add migration runner (golang-migrate or pressly/goose)
- [ ] Update database initialization code
- [ ] Update SQL queries (? → $1)
- [ ] Implement graceful shutdown
- [ ] Add connection pool configuration
- [ ] Update environment variable handling

### Phase 2: Containerization
- [ ] Update Containerfile (distroless/cc base)
- [ ] Add non-root user
- [ ] Test image builds successfully
- [ ] Verify no libc issues (test pgx connectivity)

### Phase 3: Kubernetes Manifests
- [ ] Create k8s/ directory
- [ ] Create namespace.yaml
- [ ] Create postgres-secret.yaml
- [ ] Create postgres-pvc.yaml
- [ ] Create postgres-deployment.yaml
- [ ] Create postgres-service.yaml
- [ ] Create configmap.yaml
- [ ] Create app-secret.yaml
- [ ] Create cronjob.yaml (recommended) OR deployment.yaml
- [ ] Create kustomization.yaml

### Phase 4: Local Testing
- [ ] Create docker-compose.yml
- [ ] Test local PostgreSQL connection
- [ ] Run migrations locally
- [ ] Test full scrape cycle
- [ ] Test with Kind or K3d
- [ ] Verify Kubernetes deployment

### Phase 5: CI/CD Updates
- [ ] Update GitHub Actions workflow if needed
- [ ] Update build commands
- [ ] Add migration image build (optional)

---

## 10. Library Recommendations Summary

### Required Dependencies

| Library | Purpose | Recommended Version | Notes |
|---------|---------|---------------------|-------|
| `github.com/jackc/pgx/v5` | PostgreSQL driver | v5.7.1 | Pure Go, preferred over lib/pq |
| `github.com/jackc/pgx/v5/pgxpool` | Connection pooling | v5.7.1 | Built-in connection pool |
| `github.com/golang-migrate/migrate/v4` | Schema migrations | v4.18.1 | CLI + Go library |
| `github.com/jackc/pgx/v5/stdlib` | sql.DB compatibility | v5.7.1 | For using sqlx if needed |

### Optional Dependencies

| Library | Purpose | Use Case |
|---------|---------|----------|
| `github.com/jmoiron/sqlx` | SQL extensions | If you want named queries |
| `github.com/Masterminds/squirrel` | Query builder | Dynamic SQL, type-safe |
| `github.com/lib/pq` | Alternative driver | Legacy projects (pgx recommended) |
| `github.com/pressly/goose` | Alternative migrations | Pure Go alternative to golang-migrate |

### Migration Tools Comparison

| Tool | Pure Go | Transactions | Versioning | CLI |
|------|---------|--------------|------------|-----|
| golang-migrate | ❌ (CLI binary) | ✅ | Timestamp | ✅ |
| pressly/goose | ✅ | ✅ | Sequential | ✅ |
| jackc/tern | ✅ | ✅ | Sequential | ✅ |

**Recommendation:** golang-migrate for flexibility, pressly/goose for pure Go.

---

## 11. Risk Assessment & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| SQLite → PG SQL syntax errors | High | Medium | Comprehensive testing, SQL comparison table |
| Connection pool exhaustion | Low | High | Proper pool sizing, monitoring |
| Migration failures | Low | High | Test migrations in staging, backup strategy |
| Data loss during migration | Low | Critical | SQLite is scraped data (recreatable), backup existing DB |
| Container image bloat | Low | Low | Use distroless, multi-stage builds |
| PostgreSQL resource limits | Medium | Medium | Set appropriate requests/limits |
| Secrets exposure | Low | Critical | Use Kubernetes Secrets, consider sealed-secrets |

---

## 12. Migration Order of Operations

```
1. Preparation Phase
   ├── Review this document
   ├── Set up local PostgreSQL (Docker)
   └── Install golang-migrate CLI

2. Code Changes
   ├── Update go.mod (add pgx, remove sqlite)
   ├── Create migrations/
   ├── Update database initialization
   ├── Update SQL queries
   └── Add graceful shutdown

3. Container Updates
   ├── Update Containerfile
   ├── Test local build
   └── Verify pgx connectivity

4. Kubernetes Setup
   ├── Create k8s/ manifests
   ├── Generate secrets (locally)
   └── Test with Kind/K3d

5. Deployment Phase
   ├── Deploy PostgreSQL to cluster
   ├── Run migrations
   ├── Deploy application
   └── Monitor logs and metrics
```

---

## 13. Appendix: Connection String Examples

### Development
```
postgres://agroscrapper:localdev@localhost:5432/agroscrapper?sslmode=disable
```

### Production (Kubernetes)
```
postgres://agroscrapper_user:${PASSWORD}@postgres:5432/agroscrapper?sslmode=disable
```

### Production (with SSL)
```
postgres://agroscrapper_user:${PASSWORD}@postgres:5432/agroscrapper?sslmode=require
```

### Production (Cloud managed - e.g., AWS RDS)
```
postgres://username:${PASSWORD}@my-db.abc123.us-east-1.rds.amazonaws.com:5432/agroscrapper?sslmode=require
```

---

## 14. Summary

This architecture document provides a comprehensive plan to migrate the agroscrapper-bot from SQLite to PostgreSQL and deploy it on Kubernetes. Key takeaways:

1. **Use pgx/v5** - Pure Go PostgreSQL driver with built-in pooling
2. **Use distroless/cc** - Minimal secure base image for containers
3. **Use CronJob** - More appropriate than Deployment for scrapers
4. **Use golang-migrate** - Industry-standard migration tool
5. **Use Docker Compose** - For local development and testing
6. **Use Kustomize** - For Kubernetes manifest management

The migration maintains the simplicity of the original application while adding production-ready capabilities: persistent storage, horizontal scalability potential, and Kubernetes-native deployment patterns.

---

*Document Version: 1.0*
*Created: 2026-03-12*
*Scope: SQLite to PostgreSQL migration + Kubernetes deployment preparation*
