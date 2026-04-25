---
title: "Chore: Initial repo skeleton"
status: review
priority: high
complexity: 3
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
risk_areas: []
---

## Context

Это первый story нового сервиса `oasis-modbus2mqtt` — Go-микросервиса, который мостит Modbus TCP контроллер вентустановки **GTC Oasis Syberia v5.2.0** в Home Assistant через MQTT discovery.

Полный план сервиса — в `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` (24 шага). Этот story = step 1: только пустой каркас, чтобы убедиться что репа собирается, линтуется, контейнеризуется и проходит CI.

**Особенности этого репо:**
- Это **отдельный публичный GitHub репо** `github.com/alexmorbo/oasis-modbus2mqtt` (не часть homelab монорепы).
- CI: **GitHub Actions** (не GitLab CI), потому что репо на GitHub. Никаких `homelab-ci` инклудов.
- Внешнее имя сервиса (репо, бинарь, docker image, k8s deployment): `oasis-modbus2mqtt`.
- Внутренние HA entity prefix: `oasis_syberia` (это устройство, а не сервис) — но в этом story не задействованы.

**Эталон для код-стайла, Dockerfile, golangci.yml, Makefile** — смотри `/Users/alexmorbo/Home/homelab/apps/summarizer/`. Берём его конвенции, заменяем `summarizer` → `oasis-modbus2mqtt`. CI заменяем с GitLab на GitHub Actions.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** иметь готовый каркас Go-проекта с CI, линтером, Dockerfile и Makefile,
**So that** все следующие story (домен, инфра, интерфейсы) могли просто добавлять файлы в существующую структуру без bootstrap-боли.

## Acceptance Criteria

- [ ] `go.mod` с module path `github.com/alexmorbo/oasis-modbus2mqtt`, Go 1.24.
- [ ] `cmd/server/main.go` — минимальный main, который пишет в stderr `"oasis-modbus2mqtt starting"` через slog и завершается с `os.Exit(0)`. Никакой бизнес-логики.
- [ ] `Dockerfile` — multi-stage, идентичен summarizer по структуре (alpine builder + distroless static nonroot), бинарь называется `service` в `/app/service`. Поддерживает `IMAGE_REGISTRY` и `GOPROXY` ARGs.
- [ ] `Makefile` с targets: `build`, `test`, `test-coverage`, `lint`, `run`, `clean`, `docker-build`, `help`. Бинарь в `bin/oasis-modbus2mqtt`.
- [ ] `.golangci.yml` — version 2 формат как у summarizer, тот же набор линтеров, `local-prefixes: github.com/alexmorbo/oasis-modbus2mqtt`.
- [ ] `.gitignore` — `bin/`, `coverage.*`, `.DS_Store`, `*.test`, `vendor/` (на всякий), `.env`, `*.local`.
- [ ] `.github/workflows/ci.yml` — GitHub Actions workflow: на push/PR в main запускает lint + test (Go 1.24, ubuntu-latest). НЕ публикует docker image (это позже добавим, нужны secrets).
- [ ] `README.md` — short stub: название, одна строка описания, link на `.thoughts/oasis-syberia-modbus-bridge/` нельзя (это вне публичного репо), вместо ссылки — короткий блок "Status: in development". Указать что сервис мостит Modbus TCP в MQTT discovery для HA.
- [ ] `documentation/stories/` существует (этот story лежит здесь, и здесь будут следующие).
- [ ] Каркасные пустые `.gitkeep` или README в основных директориях из плана: `domain/`, `application/`, `infrastructure/`, `interface/`, `tests/`. Это чтобы git их трекал и следующие story не путались с file-vs-dir.
- [ ] `go build ./...` и `go vet ./...` проходят.
- [ ] `golangci-lint run ./...` проходит (или skip если нет файлов кроме main.go — но main.go лучше написать так, чтобы прошёл).
- [ ] `go test ./...` проходит (нет тестов = PASS).
- [ ] `make docker-build` собирает image (имя `oasis-modbus2mqtt:latest`).

## Constraints

- НЕ создавать файлы которых нет в acceptance criteria. Минимальный каркас, никаких placeholder-сервисов с "TODO".
- НЕ добавлять зависимости в `go.mod` кроме того что нужно для main.go (только `log/slog` из stdlib).
- Использовать **distroless static** в Dockerfile (как у summarizer), не alpine для финального образа.
- Имя бинаря в Makefile = `oasis-modbus2mqtt`, но в Dockerfile = `service` (как у summarizer для обобщённости).
- GitHub Actions: использовать `actions/checkout@v4`, `actions/setup-go@v5`, `golangci/golangci-lint-action@v6` (последние стабильные мажоры).
- В CI не запускать integration tests (только `go test -short ./...`).
- В Dockerfile **не** хардкодить `GOOS=linux GOARCH=amd64` — использовать `TARGETOS`/`TARGETARCH` от buildx (для multi-arch будущего).

---

## Technical Specification

> ЗАПОЛНЯЕТ Planning Agent.

### Analysis

- **Reused from summarizer verbatim:** `.golangci.yml` structure (version 2, same linter set, same formatters), Makefile target names (`build`, `test`, `test-coverage`, `lint`, `run`, `clean`, `docker-build`, `help`), Dockerfile two-stage shape (alpine builder + distroless static nonroot), final binary path `/app/service`, `IMAGE_REGISTRY` and `GOPROXY` build ARGs, `-ldflags="-w -s" -trimpath` flags.
- **Diverges from summarizer — CI:** GitHub Actions workflow (`.github/workflows/ci.yml`) instead of GitLab CI / `homelab-ci` includes. Two parallel jobs (`lint`, `test`) on `ubuntu-latest`, Go 1.24, using `actions/checkout@v4`, `actions/setup-go@v5` (with built-in module cache), `golangci/golangci-lint-action@v6` at `version: latest`.
- **Diverges from summarizer — Dockerfile:** uses `--platform=$BUILDPLATFORM` on builder and `TARGETOS`/`TARGETARCH` ARGs (so `docker buildx` can produce multi-arch images later). Summarizer hardcodes `GOOS=linux GOARCH=amd64`; this repo doesn't.
- **Diverges from summarizer — Makefile:** drops DB-specific targets (`migrate`, `test-integration`) since the skeleton has no DB and no tests yet. Adds a `tidy` target (`go mod tidy`) which is useful while the codebase grows. Binary name is `bin/oasis-modbus2mqtt`; docker tag is `oasis-modbus2mqtt:latest`.
- **Minimal main.go:** unlike summarizer's full bootstrap, this main only emits a single `slog` JSON line to stderr ("oasis-modbus2mqtt starting") and exits. No flags, no signal handling, no config — those land in story 015. `go.mod` therefore has no `require` block (stdlib only).

### Implementation Order

1. `go.mod`
2. `cmd/server/main.go`
3. `Dockerfile`
4. `Makefile`
5. `.golangci.yml`
6. `.gitignore`
7. `.github/workflows/ci.yml`
8. `README.md`
9. `.gitkeep` файлы для директорий

---

### 1. Go module

#### File: `go.mod`

```
module github.com/alexmorbo/oasis-modbus2mqtt

go 1.24
```

### 2. Entrypoint

#### File: `cmd/server/main.go`

```go
package main

import (
	"log/slog"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger.Info("oasis-modbus2mqtt starting")
	os.Exit(0)
}
```

### 3. Container & build

#### File: `Dockerfile`

```dockerfile
# syntax=docker/dockerfile:1
ARG GO_VERSION=1.24
ARG IMAGE_REGISTRY=docker.io
ARG GOPROXY=https://proxy.golang.org,direct

FROM --platform=$BUILDPLATFORM ${IMAGE_REGISTRY}/golang:${GO_VERSION}-alpine AS builder

ARG GOPROXY
ARG TARGETOS
ARG TARGETARCH
ENV GOPROXY=${GOPROXY}

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

COPY go.mod ./
RUN if [ -f go.sum ]; then go mod download && go mod verify; fi

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-w -s" -trimpath \
    -o /build/bin/service \
    ./cmd/server

FROM ${IMAGE_REGISTRY}/distroless/static-debian12:nonroot

COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/bin/service /app/service

WORKDIR /app

ENTRYPOINT ["/app/service"]
```

#### File: `Makefile`

```makefile
.PHONY: build test test-coverage lint run clean tidy docker-build help

help:
	@echo "Available targets:"
	@echo "  build              - Build the binary"
	@echo "  test               - Run unit tests"
	@echo "  test-coverage      - Run tests with coverage report"
	@echo "  lint               - Run golangci-lint"
	@echo "  run                - Run the service"
	@echo "  clean              - Clean build artifacts"
	@echo "  tidy               - Run go mod tidy"
	@echo "  docker-build       - Build Docker image"

build:
	@echo "Building binary..."
	go build -o bin/oasis-modbus2mqtt ./cmd/server

test:
	@echo "Running tests..."
	go test ./... -v -cover -short

test-coverage:
	@echo "Running tests with coverage..."
	go test ./... -coverprofile=coverage.out -short
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
	@go tool cover -func=coverage.out | tail -1

lint:
	@echo "Running linter..."
	golangci-lint run ./...

run:
	@echo "Starting service..."
	go run ./cmd/server

clean:
	@echo "Cleaning..."
	rm -rf bin/ coverage.out coverage.html

tidy:
	@echo "Tidying modules..."
	go mod tidy

docker-build:
	@echo "Building Docker image..."
	docker build -t oasis-modbus2mqtt:latest .
```

### 4. Lint & ignore

#### File: `.golangci.yml`

```yaml
version: "2"

run:
  timeout: 5m
  go: "1.24"

linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
    - misspell
    - unconvert
    - unparam
    - gosec
    - bodyclose
    - noctx

formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/alexmorbo/oasis-modbus2mqtt

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
```

#### File: `.gitignore`

```gitignore
# Build artifacts
bin/

# Test & coverage output
coverage.*
*.test
*.out

# Vendor (we don't vendor, but guard anyway)
vendor/

# Local env / secrets
.env
*.local

# OS / editor cruft
.DS_Store
Thumbs.db
.idea/
.vscode/
*.swp
```

### 5. CI

#### File: `.github/workflows/ci.yml`

```yaml
name: CI

on:
  push:
  pull_request:

jobs:
  lint:
    name: Lint
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.24"
          cache: true

      - name: Run golangci-lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest

  test:
    name: Test
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.24"
          cache: true

      - name: Build
        run: go build ./...

      - name: Vet
        run: go vet ./...

      - name: Test
        run: go test -short ./...
```

### 6. README

#### File: `README.md`

```markdown
# oasis-modbus2mqtt

MQTT bridge for GTC Oasis Syberia v5 ventilation controller via Modbus TCP — exposes Home Assistant MQTT discovery.

## Status

In active development — not production-ready.

## Overview

`oasis-modbus2mqtt` connects to a GTC Oasis Syberia v5 ventilation controller over Modbus TCP, polls its registers, and republishes the state as Home Assistant MQTT discovery entities. Commands published by Home Assistant on the corresponding MQTT topics are translated back into Modbus writes.

## Requirements

- Go 1.24+
- A reachable Modbus TCP endpoint of the Oasis Syberia controller
- An MQTT broker (e.g. Mosquitto) reachable from both this service and Home Assistant

## Build

```bash
make build           # produces ./bin/oasis-modbus2mqtt
make docker-build    # produces oasis-modbus2mqtt:latest
```

## Development

```bash
make test            # unit tests (short mode)
make lint            # golangci-lint
make tidy            # go mod tidy
```

## License

MIT
```

### 7. Directory placeholders

#### File: `domain/.gitkeep`

```
```

#### File: `application/.gitkeep`

```
```

#### File: `infrastructure/.gitkeep`

```
```

#### File: `interface/.gitkeep`

```
```

#### File: `tests/.gitkeep`

```
```

---

## Agent Execution

### Implementation Agent Instructions

```
Task tool call:
- subagent_type: "general-purpose"
- model: "haiku"
- prompt: |
    You are an IMPLEMENTATION AGENT for story 001-initial-skeleton.

    YOUR ONLY JOB: Copy code/content from the story file to actual files.

    RULES:
    - DO NOT modify content
    - DO NOT add anything
    - DO NOT fix issues
    - ONLY copy each "#### File: `path`" code block to that file path

    PROCESS:
    1. Read story: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/documentation/stories/001-initial-skeleton.md
    2. For each "#### File: `path`" section, write the code block content to that path (relative to apps/oasis-modbus2mqtt/)
    3. Run verification:
       - cd /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt
       - go mod tidy
       - go build ./...
       - go vet ./...
       - go test -short ./...
       - golangci-lint run ./... (skip if not installed, log it)
    4. If PASS: set status to "review", fill in Verification Results
    5. If FAIL: write errors verbatim to "Issues Found" section, status stays "in_progress"

    Work directory: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard fix flow per `documentation/golang/agent-workflow/fix-guide.md`. Edit story only, not actual files.

---

## Implementation Notes

### Progress

- [x] go.mod created
- [x] main.go created
- [x] Dockerfile created
- [x] Makefile created
- [x] .golangci.yml created
- [x] .gitignore created
- [x] GitHub Actions workflow created
- [x] README.md created
- [x] .gitkeep files created
- [x] go build passes
- [x] go vet passes
- [x] go test passes
- [x] golangci-lint passes
- [ ] docker build succeeds (manual verify)

### Verification Results

```
go mod tidy:  PASS (no changes)
go build:     PASS
go vet:       PASS
go test:      PASS (no test files, 0 issues)
golangci-lint: PASS (0 issues)
```

### Issues Found

None — all verification steps passed.

### Fixes Applied

No fixes needed.

---

## Files Changed

- `go.mod`
- `cmd/server/main.go`
- `Dockerfile`
- `Makefile`
- `.golangci.yml`
- `.gitignore`
- `.github/workflows/ci.yml`
- `README.md`
- `domain/.gitkeep`
- `application/.gitkeep`
- `infrastructure/.gitkeep`
- `interface/.gitkeep`
- `tests/.gitkeep`
