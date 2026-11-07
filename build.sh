#!/bin/bash
# Render Build Script — Pure Go Engine cho Render.com (Tich hop san Chromium Headless)
set -e

export CGO_ENABLED=0

echo "[BUILD] 1/1 Bien dich Pure Go Engine cho Linux/AMD64..."
cd muse_router_engine
go build -ldflags="-s -w" -o ../app .
cd ..
echo "[BUILD] Build hoan tat thanh cong! Chromium duoc cai trong Docker image."
echo "[BUILD] File thuc thi: $(ls -lh app | awk '{print $9, $5}')"
