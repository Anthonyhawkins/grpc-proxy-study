#!/bin/bash
set -e

echo "Cleaning up old processes..."
lsof -i :9090 -t | xargs kill -9 2>/dev/null || true
lsof -i :8080 -t | xargs kill -9 2>/dev/null || true
sleep 1

echo "Starting Backend..."
go run backend/main.go &
BACKEND_PID=$!

sleep 2

echo "--- Testing with PB Descriptor Method ---"
go run proxy/main.go -method=pb -pb=api/echo/echo.pb &
PROXY_PID=$!

sleep 2

go run client/main.go

lsof -i :8080 -t | xargs kill -9 2>/dev/null || true
sleep 2

echo "--- Testing with Reflection Method ---"
go run proxy/main.go -method=reflect &
PROXY_PID2=$!

sleep 2

go run client/main.go

kill $PROXY_PID2
kill $BACKEND_PID
echo "Tests completed successfully."
