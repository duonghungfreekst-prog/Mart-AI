#!/bin/bash
# Render Build Script — biên dịch Go Engine + tải Chrome Headless Shell tự động (0 root)
set -e

export CGO_ENABLED=0

echo "[BUILD] 1/3 Building muse_router_engine for linux/amd64..."
cd muse_router_engine
go build -ldflags="-s -w" -o ../app .

echo "[BUILD] 2/3 Provisioning Chrome Headless Shell for server-side login & chat..."
go run ./cmd/setup_chrome ../chrome_bin || echo "[BUILD] Warning: setup_chrome skipped"
cd ..

CHROME_EXE="./chrome_bin/chrome-headless-shell-linux64/chrome-headless-shell"
if [ -x "$CHROME_EXE" ]; then
  echo "[BUILD] 3/3 Checking shared libraries & fonts for Chrome Headless Shell..."
  NEED_LIBS=0
  if ldd "$CHROME_EXE" 2>/dev/null | grep -q "not found"; then
    NEED_LIBS=1
  fi
  NEED_FONTS=0
  if [ ! -d "/usr/share/fonts/truetype" ] || [ -z "$(find /usr/share/fonts -name '*.ttf' 2>/dev/null | head -n 1)" ]; then
    NEED_FONTS=1
  fi

  if [ "$NEED_LIBS" = "1" ] || [ "$NEED_FONTS" = "1" ]; then
    echo "[BUILD] Provisioning user-space Debian runtime libs/fonts..."
    mkdir -p ./apt_state/lists/partial ./apt_cache/archives/partial ./chrome_libs
    apt-get -o Dir::State=./apt_state -o Dir::State::status=/var/lib/dpkg/status -o Dir::Cache=./apt_cache update -qq || true
    PKGS="fonts-dejavu-core"
    if [ "$NEED_LIBS" = "1" ]; then
      PKGS="$PKGS libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libdbus-1-3 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libasound2 libpango-1.0-0 libcairo2 libatspi2.0-0 libx11-6 libxext6 libxcb1 libxshmfence1 libexpat1"
    fi
    for pkg in $PKGS; do
      apt-get -o Dir::State=./apt_state -o Dir::State::status=/var/lib/dpkg/status -o Dir::Cache=./apt_cache \
        download "$pkg" >/dev/null 2>&1 || \
      apt-get -o Dir::State=./apt_state -o Dir::State::status=/var/lib/dpkg/status -o Dir::Cache=./apt_cache \
        download "${pkg}t64" >/dev/null 2>&1 || true
    done
    for deb in ./*.deb ./apt_cache/archives/*.deb; do
      if [ -f "$deb" ]; then
        dpkg-deb -x "$deb" ./chrome_libs || true
        rm -f "$deb"
      fi
    done
    rm -rf ./apt_state ./apt_cache
  fi
  export LD_LIBRARY_PATH="$PWD/chrome_libs/usr/lib/x86_64-linux-gnu:$PWD/chrome_libs/lib/x86_64-linux-gnu:${LD_LIBRARY_PATH:-}"
  "$CHROME_EXE" --version || true
fi

echo "[BUILD] Done! Binary: $(ls -lh app)"
