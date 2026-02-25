.PHONY: all setup clean run-backend run-proxy-pb run-proxy-reflect run-client test-pb test-reflect

all: setup

setup:
	@echo "Installing dependencies and compiling protobufs..."
	go mod download
	export PATH="$$PATH:$$(go env GOPATH)/bin" && \
		protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		--descriptor_set_out=api/echo/echo.pb \
		--include_imports api/echo/echo.proto
	@echo "Setup complete."

clean:
	@echo "Cleaning up processes..."
	-lsof -i :9090 -t | xargs kill -9 2>/dev/null || true
	-lsof -i :8080 -t | xargs kill -9 2>/dev/null || true
	@echo "Clean complete."

run-backend:
	@echo "Starting Backend Server on :9090..."
	go run backend/main.go

run-proxy-pb:
	@echo "Starting Proxy Server (PB mode) on :8080..."
	go run proxy/main.go -method=pb -pb=api/echo/echo.pb

run-proxy-reflect:
	@echo "Starting Proxy Server (Reflection mode) on :8080..."
	go run proxy/main.go -method=reflect

run-client:
	@echo "Starting Test Client..."
	go run client/main.go

# Helpers to run full tests (similar to test.sh)
test-pb: clean
	@echo "--- Testing PB Mode ---"
	@make run-backend &
	@sleep 2
	@make run-proxy-pb &
	@sleep 2
	@make run-client
	@make clean

test-reflect: clean
	@echo "--- Testing Reflection Mode ---"
	@make run-backend &
	@sleep 2
	@make run-proxy-reflect &
	@sleep 2
	@make run-client
	@make clean
