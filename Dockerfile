# Multi-stage build cho Render.com (Pure Go Engine - Sieu nhe ~25MB RAM)
FROM golang:1.23-alpine AS builder

WORKDIR /build

COPY . .

WORKDIR /build/muse_router_engine
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /build/app .

FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/app ./app
COPY --from=builder /build/muse_router_engine/web ./muse_router_engine/web

RUN mkdir -p /app/data

EXPOSE 10000

ENV RENDER=true
ENV PORT=10000

CMD ["./app"]
