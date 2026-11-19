# Headless EventScreenDisplay agent, for testing a screen manager (such as
# RCTC's) without a Pi. It runs the real API, playlist, messages and stage
# with the dummy browser, so nothing is drawn. Wi-Fi, mDNS and the HDMI
# setup stay on the Pi.
#
#   docker build -t eventscreen:dev .          (or: make docker)
#   docker run --rm -p 8080:8080 eventscreen:dev
#
# Pass a config with -v ./config.toml:/etc/eventscreen/config.toml:ro to set
# screen_id, name or api_token. Keep browser = "dummy" in it.

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS=linux TARGETARCH TARGETVARIANT
ARG VERSION=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN set -eu; \
    ldflags="-s -w"; \
    if [ -n "$VERSION" ]; then ldflags="$ldflags -X github.com/RCRaceHub/EventScreenDisplay/internal/version.Version=$VERSION"; fi; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
      go build -trimpath -ldflags "$ldflags" -o /out/eventscreen ./cmd/eventscreen; \
    mkdir -p /out/state

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/eventscreen /usr/bin/eventscreen
COPY --from=build --chown=65534:65534 /out/state /state
USER 65534:65534
EXPOSE 8080
VOLUME /state
ENTRYPOINT ["/usr/bin/eventscreen"]
CMD ["run", "--browser", "dummy", "--state-dir", "/state"]
