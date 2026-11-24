# EventScreenDisplay viewer agent.
#
#   make              build for this machine (./eventscreen)
#   make test         unit and API tests (make race / make cover for more)
#   make arm64        static binaries for each architecture, in dist/bin/
#   make armhf
#   make amd64
#   make all-arch     all three
#   make deb-arm64    the .deb for one architecture, in dist/
#   make deb-armhf
#   make deb-amd64
#   make debs         all three .debs
#   make docker       the headless test image (eventscreen:dev)
#   make lint         gofmt, go vet and shellcheck
#
# VERSION defaults to the one in internal/version/version.go; release builds
# pass the tag's version: make debs VERSION=0.7.1

VERSION ?= $(shell sed -n 's/^var Version = "\(.*\)"/\1/p' internal/version/version.go)
PKG     := github.com/RCRaceHub/EventScreenDisplay
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"
SHELLS  := deploy/install.sh deploy/eventscreen-setup deploy/eventscreen-bootconfig packaging/build-deb.sh image/build-image.sh image/imager-json.sh image/check-image.sh
SHSH    := deploy/eventscreen-check packaging/debian/postinst packaging/debian/prerm packaging/debian/postrm

.PHONY: build test race cover lint arm64 armhf amd64 all-arch deb-arm64 deb-armhf deb-amd64 debs docker clean

build:
	$(GOBUILD) -o eventscreen ./cmd/eventscreen

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -race -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1

lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w ."; exit 1; }
	go vet ./...
	shellcheck -S warning $(SHELLS)
	shellcheck -S warning -s sh $(SHSH)

# Debian architecture names. armhf is ARMv6 hard-float, Raspberry Pi OS's
# 32-bit userland, which runs on every Pi.
arm64:
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/bin/eventscreen-linux-arm64 ./cmd/eventscreen
armhf:
	GOOS=linux GOARCH=arm GOARM=6 $(GOBUILD) -o dist/bin/eventscreen-linux-armhf ./cmd/eventscreen
amd64:
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/bin/eventscreen-linux-amd64 ./cmd/eventscreen

all-arch: arm64 armhf amd64

DEB = ARCH=$(1) BIN=dist/bin/eventscreen-linux-$(1) packaging/build-deb.sh $(VERSION)
deb-arm64: arm64
	$(call DEB,arm64)
deb-armhf: armhf
	$(call DEB,armhf)
deb-amd64: amd64
	$(call DEB,amd64)

debs: deb-arm64 deb-armhf deb-amd64

docker:
	docker build --build-arg VERSION=$(VERSION) -t eventscreen:dev .

clean:
	rm -f eventscreen coverage.out
	rm -fr dist/bin dist/*.deb
