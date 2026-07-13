FROM golang:1.23-bookworm AS builder
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY . .
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/tracker ./cmd/tracker

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/tracker /usr/local/bin/tracker
RUN mkdir -p /app/data
WORKDIR /app
# The daemon touches /app/data/heartbeat after every completed tick (180s);
# consider the container unhealthy when it goes quiet for 10+ minutes.
HEALTHCHECK --interval=60s --timeout=5s --start-period=5m \
  CMD sh -c '[ -n "$(find /app/data/heartbeat -mmin -10 2>/dev/null)" ]'
ENTRYPOINT ["/usr/local/bin/tracker"]
