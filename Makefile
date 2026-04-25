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
