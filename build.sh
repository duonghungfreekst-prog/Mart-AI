#!/bin/bash
# Render Build Script — Pure Go Engine cho Render.com (Sieu nhe ~25MB RAM, 0 CGO, 0 bloat)
set -e

export CGO_ENABLED=0

echo "[BUILD] 1/2 Bien dich Pure Go Engine cho Linux/AMD64..."
cd muse_router_engine
go build -ldflags="-s -w" -o ../app .
cd ..

echo "[BUILD] 2/2 Bien dich hoan tat thanh cong!"
echo "[BUILD] File thuc thi: $(ls -lh app | awk '{print $9, $5}')"
