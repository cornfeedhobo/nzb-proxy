FROM golang:1.26.11-alpine AS build
WORKDIR /src
RUN mkdir -m 0700 /data
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${REVISION}" -o /nzb-proxy ./cmd/nzb-proxy

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=10001:10001 /data/ /data/
COPY --from=build /nzb-proxy /usr/local/bin/nzb-proxy
USER 10001:10001
ENV NZB_PROXY_CACHE_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nzb-proxy"]
