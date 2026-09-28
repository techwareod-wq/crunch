# syntax=docker/dockerfile:1.7

# ---------- Build stage ----------
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/crunch ./cmd/service

# ---------- Runtime stage ----------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S app && adduser -S app -G app

ENV TZ=UTC \
    PORT=3090 \
    ENVIRONMENT=production

WORKDIR /app

COPY --from=builder /out/crunch /app/crunch
# Runtime tunables read by config.LoadValues at startup (values/<env>/values.yaml).
COPY --from=builder /src/values /app/values

USER app

EXPOSE 3090

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:3090/health || exit 1

ENTRYPOINT ["/app/crunch"]
