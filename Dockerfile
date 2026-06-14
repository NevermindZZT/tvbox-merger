# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/tvbox-merger .

# Stage 2: Runtime
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/tvbox-merger .
COPY --from=builder /app/web ./web

# Create data and cache directories
RUN mkdir -p /app/data /app/data/cache

EXPOSE 8080

ENV PORT=8080
ENV DATA_SOURCE=/app/data/tvbox.db
ENV CACHE_DIR=/app/data/cache

ENTRYPOINT ["./tvbox-merger"]
