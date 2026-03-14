# AgroScrapper Bot

A web scraper and notification bot that monitors agricultural training courses from [Formación Agraria Tenerife](https://formacionagraria.tenerife.es/) and sends Telegram notifications when new courses are available.

## Features

- **Web Scraping**: Automatically scrapes course information from the training portal
- **PostgreSQL Database**: Persistent storage for tracking seen courses
- **Telegram Notifications**: Real-time alerts when new courses are published
- **Kubernetes Ready**: Production-grade deployment manifests included
- **Docker Support**: Multi-stage builds with distroless runtime
- **Graceful Shutdown**: Proper signal handling for clean container termination

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.23+ |
| Database | PostgreSQL 16+ |
| Web Scraper | Colly |
| Driver | pgx/v5 (pure Go) |
| Migrations | golang-migrate |
| Container Runtime | Distroless (gcr.io/distroless/cc) |
| Orchestration | Kubernetes (CronJob) |

## Prerequisites

- Go 1.23 or later
- PostgreSQL 16+ (or Docker for local development)
- Telegram Bot Token and Chat ID
- kubectl (for Kubernetes deployment)
- Docker (for building containers)

## Installation

### Local Development

1. **Clone the repository**
   ```bash
   git clone https://github.com/bender-916/agroscrapper-k8s.git
   cd agroscrapper-k8s
   ```

2. **Set environment variables**
   ```bash
   export TELEGRAM_TOKEN="your_bot_token"
   export TELEGRAM_CHATID="your_chat_id"
   export TELEGRAM_THREADID="your_thread_id"  # Optional
   export DATABASE_URL="postgres://user:password@localhost:5432/agroscrapper?sslmode=disable"
   ```

3. **Run with Docker Compose** (recommended)
   ```bash
   docker-compose up --build
   ```

4. **Or run locally with Go**
   ```bash
   # Start PostgreSQL first
   go mod download
   go run main.go -migrate  # Run migrations
   go run main.go           # Run the scraper
   ```

### Build Container Image

```bash
docker build -f Containerfile -t agroscrapper-bot:latest .
```

## Deployment

### Kubernetes

The `k8s/` directory contains all necessary manifests for deploying to Kubernetes:

```bash
# Apply all manifests
kubectl apply -k k8s/

# Or apply individually
kubectl apply -f k8s/00-namespace.yaml
kubectl apply -f k8s/01-postgres-secret.yaml
kubectl apply -f k8s/02-postgres-pvc.yaml
kubectl apply -f k8s/03-postgres-deployment.yaml
kubectl apply -f k8s/04-postgres-service.yaml
kubectl apply -f k8s/05-configmap.yaml
kubectl apply -f k8s/06-app-secret.yaml
kubectl apply -f k8s/09-cronjob.yaml
```

**Important**: Update the secrets in `k8s/01-postgres-secret.yaml` and `k8s/06-app-secret.yaml` before deploying to production.

### CronJob Schedule

The scraper runs as a Kubernetes CronJob. Default schedule: `0 9 * * *` (daily at 9 AM Atlantic/Canary timezone).

Modify the schedule in `k8s/09-cronjob.yaml`:

```yaml
spec:
  schedule: "0 9 * * *"  # Adjust as needed
  timeZone: "Atlantic/Canary"
```

## Usage

### Command Line Flags

| Flag | Environment Variable | Description | Required |
|------|---------------------|-------------|----------|
| `-database-url` | `DATABASE_URL` | PostgreSQL connection string | Yes |
| `-token` | `TELEGRAM_TOKEN` | Telegram bot token | Yes |
| `-chatid` | `TELEGRAM_CHATID` | Telegram chat ID | Yes |
| `-threadid` | `TELEGRAM_THREADID` | Telegram thread/topic ID | No |
| `-migrate` | - | Run database migrations | No |

### Example

```bash
./agroscrapper \
  -database-url "postgres://user:pass@localhost:5432/agroscrapper?sslmode=disable" \
  -token "123456:ABC-DEF" \
  -chatid "-100123456789" \
  -migrate
```

## Project Structure

```
agroscrapper-bot/
├── main.go              # Application entry point
├── go.mod               # Go module definition
├── go.sum               # Dependency checksums
├── Containerfile        # Multi-stage Docker build
├── docker-compose.yml   # Local development setup
├── ARCHITECTURE.md      # Detailed architecture documentation
├── k8s/                 # Kubernetes manifests
│   ├── 00-namespace.yaml
│   ├── 01-postgres-secret.yaml
│   ├── 02-postgres-pvc.yaml
│   ├── 03-postgres-deployment.yaml
│   ├── 04-postgres-service.yaml
│   ├── 05-configmap.yaml
│   ├── 06-app-secret.yaml
│   ├── 09-cronjob.yaml
│   └── kustomization.yaml
├── migrations/          # Database migration files
│   ├── 000001_create_cursos_table.up.sql
│   └── 000001_create_cursos_table.down.sql
└── .github/             # GitHub workflows (if any)
```

## Database Schema

The application uses a single table to track courses:

```sql
CREATE TABLE cursos (
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
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL connection string | - |
| `TELEGRAM_TOKEN` | Bot token from @BotFather | - |
| `TELEGRAM_CHATID` | Target chat/group ID | - |
| `TELEGRAM_THREADID` | Thread/topic ID for groups | - |

### Connection Pool Settings

The application uses pgx connection pooling with sensible defaults:

- **Max Connections**: 5
- **Min Connections**: 1
- **Max Connection Lifetime**: 1 hour
- **Max Idle Time**: 30 minutes

## Contributing

Contributions are welcome! Please follow these steps:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Development Guidelines

- Follow Go standard formatting (`gofmt`)
- Add tests for new functionality
- Update documentation as needed
- Ensure migrations are reversible

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- [Colly](https://github.com/gocolly/colly) - Web scraping framework
- [pgx](https://github.com/jackc/pgx) - PostgreSQL driver for Go
- [golang-migrate](https://github.com/golang-migrate/migrate) - Database migrations
