FROM golang:1.26.4-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /nzb-proxy ./cmd/nzb-proxy

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 nzbproxy && adduser -D -H -u 10001 -G nzbproxy nzbproxy && mkdir /data && chown nzbproxy:nzbproxy /data
COPY --from=build /nzb-proxy /usr/local/bin/nzb-proxy
USER 10001:10001
ENV NZB_PROXY_CACHE_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nzb-proxy"]
