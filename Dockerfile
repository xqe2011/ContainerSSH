# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/containerssh ./cmd/containerssh

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/containerssh /usr/bin/containerssh

EXPOSE 2222

ENTRYPOINT ["/usr/bin/containerssh"]
CMD ["--config", "/etc/containerssh/config.yaml"]
