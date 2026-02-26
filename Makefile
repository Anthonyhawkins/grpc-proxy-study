.PHONY: all setup clean build-rust run-backend run-proxy-pb run-proxy-pb-rust run-client bench-all

all: setup build-rust

setup:
	@echo "Installing dependencies and compiling protobufs..."
	go mod download
	export PATH="$$PATH:$$(go env GOPATH)/bin" && \
		protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		--descriptor_set_out=api/echo/echo.pb \
		--include_imports api/echo/echo.proto
	@echo "Setup complete."

build-rust:
	@echo "Building Rust C-ABI Crypto Library..."
	cd rust-crypto && cargo build --release
	@echo "Rust Library Built."

clean:
	@echo "Cleaning up processes..."
	-lsof -i :9090 -t | xargs kill -9 2>/dev/null || true
	-lsof -i :8080 -t | xargs kill -9 2>/dev/null || true
	@echo "Clean complete."

run-backend:
	@echo "Starting Backend Server on :9090..."
	go run ./go-proxy/backend

run-proxy-pb:
	@echo "Starting Proxy Server (PB mode, Go Crypto) on :8080..."
	go run ./go-proxy/proxy -config=go-proxy/config.yaml -crypto=go

run-proxy-pb-rust:
	@echo "Starting Proxy Server (PB mode, Rust Crypto) on :8080..."
	go run ./go-proxy/proxy -config=go-proxy/config.yaml -crypto=rust

run-client:
	@echo "Starting Test Client..."
	go run ./go-proxy/client

bench-all: clean build-rust
	@echo "--- Starting Services for Benchmark ---"
	@make run-backend > /dev/null 2>&1 &
	@sleep 2
	@make run-proxy-pb > /dev/null 2>&1 &
	@echo "Waiting for services to boot..."
	@sleep 3
	
	@echo "\n=== BENCHMARK 1: Legacy Pass-Thru (No Inspection) ==="
	go run benchmark/main.go -mode=legacy -count=10000
	
	@echo "\nWaiting 5 seconds for system to settle..."
	@sleep 5

	@echo "\n=== BENCHMARK 2: Inspect Outer (Decode & Log, No Crypto) ==="
	go run benchmark/main.go -mode=inspect -count=10000
	
	@echo "\nWaiting 5 seconds for system to settle..."
	@sleep 5
	
	@echo "\n=== BENCHMARK 3: Secure Envelope (Pure Go Crypto) ==="
	go run benchmark/main.go -mode=secure -count=10000
	
	@echo "\n--- Stopping Go Proxy ---"
	-lsof -i :8080 -t | xargs kill -9 2>/dev/null || true
	@sleep 2

	@echo "\n--- Starting Proxy with Rust FFI ---"
	@make run-proxy-pb-rust > /dev/null 2>&1 &
	@sleep 3

	@echo "\n=== BENCHMARK 4: Secure Envelope (Rust FFI Crypto) ==="
	go run benchmark/main.go -mode=secure -count=10000
	
	@echo "\n--- Benchmarks Complete ---"
	@make clean
