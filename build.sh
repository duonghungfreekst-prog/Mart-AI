#!/bin/bash
# Render Build Script — Pure Go Engine cho Render.com (Tich hop san Chromium Headless)
set -e

export CGO_ENABLED=0

echo "[BUILD] 1/3 Bien dich Pure Go Engine cho Linux/AMD64..."
cd muse_router_engine
go build -ldflags="-s -w" -o ../app .
cd ..

echo "[BUILD] 2/3 Kiem tra va cai dat Chromium Headless cho Linux..."
CHROME_TARGET_DIR="chrome_bin/chrome-headless-shell-linux64"
if [ ! -f "$CHROME_TARGET_DIR/chrome-headless-shell" ]; then
    echo "[BUILD] Dang tai Google Chrome Headless Shell (Linux x64)..."
    mkdir -p chrome_bin
    curl -sSL "https://storage.googleapis.com/chrome-for-testing-public/128.0.6613.137/linux64/chrome-headless-shell-linux64.zip" -o chrome_bin/chrome.zip
    unzip -q chrome_bin/chrome.zip -d chrome_bin/
    rm -f chrome_bin/chrome.zip
    chmod +x "$CHROME_TARGET_DIR/chrome-headless-shell" || true
    echo "[BUILD] Chromium Headless Shell da san sang!"
else
    echo "[BUILD] Chromium Headless Shell da ton tai san."
fi

echo "[BUILD] 3/3 Build hoan tat thanh cong!"
echo "[BUILD] File thuc thi: $(ls -lh app | awk '{print $9, $5}')"