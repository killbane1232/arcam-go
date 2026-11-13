# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/arcam ./cmd/arcam

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -g 10001 arcam \
    && adduser -D -u 10001 -G arcam arcam \
    && mkdir /data && chown arcam:arcam /data
COPY --from=build /out/arcam /usr/local/bin/arcam
USER 10001:10001
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["arcam"]
CMD ["-addr", "0.0.0.0:8080", "-data", "/data"]
