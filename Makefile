.PHONY: build run test clean docker docker-run lint fmt bench bench-server bench-router bench-resilience bench-providers bench-cpu bench-mem bench-baseline bench-compare

# Build variables
BINARY_NAME=resillm
VERSION?=0.1.0
LDFLAGS=-ldflags "-w -s -X main.version=$(VERSION)"

# Build the binary
build:
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/resillm

# Run locally
run: build
	./bin/$(BINARY_NAME) --config config.yaml

# Run with hot reload (requires air: go install github.com/cosmtrek/air@latest)
dev:
	air

# Run tests
test:
	go test -v -race ./...

# Run tests with coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

# Build Docker image
docker:
	docker build -t resillm:$(VERSION) -t resillm:latest .

# Run with Docker Compose
docker-run:
	docker-compose up -d

# Stop Docker Compose
docker-stop:
	docker-compose down

# Lint (requires golangci-lint)
lint:
	golangci-lint run ./...

# Format code
fmt:
	go fmt ./...
	goimports -w .

# Download dependencies
deps:
	go mod download
	go mod tidy

# Generate (if needed in future)
generate:
	go generate ./...

# Install dev tools
tools:
	go install github.com/cosmtrek/air@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest

# Build for multiple platforms
release:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/resillm
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 ./cmd/resillm
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 ./cmd/resillm
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 ./cmd/resillm
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe ./cmd/resillm

# ==================== Benchmarks ====================

# Run all benchmarks
bench:
	go test -bench=. -benchmem ./internal/...

# Run server benchmarks (rate limiter, handlers, load shedding)
bench-server:
	go test -bench=. -benchmem ./internal/server

# Run router benchmarks
bench-router:
	go test -bench=. -benchmem ./internal/router

# Run resilience benchmarks (circuit breaker, retrier)
bench-resilience:
	go test -bench=. -benchmem ./internal/resilience

# Run provider benchmarks (semaphores)
bench-providers:
	go test -bench=. -benchmem ./internal/providers

# Generate CPU profile for router benchmarks
bench-cpu:
	go test -bench=BenchmarkRouter -cpuprofile=cpu.prof ./internal/router
	@echo "Run 'go tool pprof -http=:8081 cpu.prof' to view the profile"

# Generate memory profile for router benchmarks
bench-mem:
	go test -bench=BenchmarkRouter -memprofile=mem.prof ./internal/router
	@echo "Run 'go tool pprof -http=:8081 mem.prof' to view the profile"

# Save benchmark baseline (run before making changes)
bench-baseline:
	go test -bench=. -benchmem -count=5 ./internal/... > bench-old.txt
	@echo "Baseline saved to bench-old.txt"

# Compare benchmarks with baseline (run after making changes)
bench-compare:
	@if [ -f bench-old.txt ]; then \
		go test -bench=. -benchmem -count=5 ./internal/... > bench-new.txt; \
		@echo "New benchmarks saved to bench-new.txt"; \
		@echo "Install benchstat with: go install golang.org/x/perf/cmd/benchstat@latest"; \
		@echo "Then run: benchstat bench-old.txt bench-new.txt"; \
	else \
		echo "No bench-old.txt found. Run 'make bench-baseline' first."; \
	fi

# Clean benchmark artifacts
bench-clean:
	rm -f cpu.prof mem.prof bench-old.txt bench-new.txt
