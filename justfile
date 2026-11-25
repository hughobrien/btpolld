test:
    go test -race ./...

build:
    go build -o btpolld ./cmd/btpolld

# A static binary for a Raspberry Pi Zero (armv6).
build-pi:
    CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 go build -o btpolld-arm ./cmd/btpolld
