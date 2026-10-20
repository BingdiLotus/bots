# Pinned Go 1.26.9 builder; the application remains a static scratch image.
FROM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bots ./cmd/bots

FROM scratch
USER 65532:65532
COPY --from=build /out/bots /bots
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV BOTS_HTTP_ADDR=:8090
EXPOSE 8090
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 CMD ["/bots","readyz"]
ENTRYPOINT ["/bots"]
