# Multi-stage build cho Render.com (Pure Go Engine tích hợp sẵn Chromium Headless Server-side)
FROM golang:1.23-alpine AS builder

WORKDIR /build

COPY . .

WORKDIR /build/muse_router_engine
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /build/app .

FROM alpine:3.19

# Cài đặt Chromium headless và font chữ đầy đủ để Render tự động chạy ngầm trình duyệt
RUN apk --no-cache add \
    ca-certificates \
    tzdata \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ttf-freefont \
    font-noto-emoji

WORKDIR /app

COPY --from=builder /build/app ./app
COPY --from=builder /build/muse_router_engine/web ./muse_router_engine/web

RUN mkdir -p /app/data

EXPOSE 10000

ENV RENDER=true
ENV PORT=10000
ENV CHROME_BIN=/usr/bin/chromium-browser

CMD ["./app"]
