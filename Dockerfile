# Multi-stage build cho Render.com
# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Copy toàn bộ repo
COPY . .

# Build engine từ muse_router_engine/
WORKDIR /build/muse_router_engine
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /build/muse_router_srv .

# Stage 2: Runtime image nhỏ gọn
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy binary
COPY --from=builder /build/muse_router_srv ./muse_router_srv

# Copy static web assets
COPY --from=builder /build/muse_router_engine/web ./muse_router_engine/web

# data dir
RUN mkdir -p /app/data

EXPOSE 10000

ENV RENDER=true
ENV PORT=10000

CMD ["./muse_router_srv"]
