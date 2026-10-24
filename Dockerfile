FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:290a54a1f70d617560f843301bcb32b339f1687e8e0bff49ebc043903aeb19d8 AS builder
ARG TARGETOS TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/simplemusicserver . && mkdir -p /out/data

FROM litestream/litestream:0.5.17@sha256:4b02b9859a6b6b4087d8b8944e15f7e984bd7957cba322bbeee38b0e27b9656a AS litestream

FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087

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
