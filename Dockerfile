FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /tailhop ./cmd/tailhop

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/lib/tailhop && chown 10001:10001 /var/lib/tailhop && chmod 0700 /var/lib/tailhop
COPY --from=build /tailhop /usr/local/bin/tailhop
COPY LICENSE THIRD_PARTY_NOTICES /usr/share/doc/tailhop/
USER 10001:10001
ENV TS_STATE_DIR=/var/lib/tailhop
VOLUME ["/var/lib/tailhop"]
ENTRYPOINT ["/usr/local/bin/tailhop"]
