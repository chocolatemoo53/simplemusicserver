FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d AS builder
ARG TARGETOS TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/simplemusicserver . && mkdir -p /out/data

FROM litestream/litestream:0.5.18@sha256:513bff62a88fc0c86c73d27333c41c54c89fe890a7c32a551600adff9309b11b AS litestream

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

COPY --from=builder /out/simplemusicserver /app/simplemusicserver
COPY --from=builder /src/assets /app/assets
COPY --from=builder --chown=nonroot:nonroot /out/data /data
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY litestream.yml /etc/litestream.yml

USER nonroot:nonroot
WORKDIR /app
VOLUME /data
EXPOSE 8000

ENTRYPOINT ["/usr/local/bin/litestream", "replicate", "-restore-if-db-not-exists", "-exec", "/app/simplemusicserver -cfg /config/config.toml -addr :8000"]
