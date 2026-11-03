#!/bin/bash
# Render Build Script - tự động build từ muse_router_engine
set -e

export CGO_ENABLED=0

echo "[BUILD] Building muse_router_engine for linux/amd64..."
cd muse_router_engine
go build -ldflags="-s -w" -o ../app .
cd ..

echo "[BUILD] Done! Binary: $(ls -lh app)"
