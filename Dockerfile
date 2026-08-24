# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out/data && chown 65532:65532 /out/data && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/sb-manager-bot ./cmd/sb-manager-bot

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/krabdo/sb-manager-bot" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/sb-manager-bot /sb-manager-bot
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/sb-manager-bot", "healthcheck"]
ENTRYPOINT ["/sb-manager-bot"]
