# Build
FROM golang:1.24-bookworm AS builder

WORKDIR /src
ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0 \
    GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/mapper .

# Runtime (Chromium for chromedp)
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      chromium \
      ca-certificates \
      fonts-liberation \
      fonts-noto-core \
      fonts-noto-color-emoji \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/mapper /app/mapper

RUN mkdir -p /app/tmp/debug \
 && useradd --create-home --shell /usr/sbin/nologin app \
 && chown -R app:app /app

USER app

ENV CHROME_PATH=/usr/bin/chromium \
    HOME=/app

EXPOSE 8080

CMD ["/app/mapper"]
