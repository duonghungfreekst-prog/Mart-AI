#!/bin/bash
# Render Build Script - runs from repo root
set -e

echo "[BUILD] Setting up Go environment..."
export CGO_ENABLED=0

echo "[BUILD] Building muse_router_engine for linux/amd64..."
cd muse_router_engine
go build -ldflags="-s -w" -o ../muse_router_srv .
cd ..

echo "[BUILD] Done! Binary: $(ls -lh muse_router_srv)"
