FROM golang:1.26.0 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bots ./cmd/bots

FROM scratch
USER 65532:65532
COPY --from=build /out/bots /bots
ENV BOTS_HTTP_ADDR=:8090
EXPOSE 8090
ENTRYPOINT ["/bots"]
