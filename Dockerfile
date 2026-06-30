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
ENTRYPOINT ["/usr/local/bin/tracker"]
