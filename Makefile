.PHONY: run test build linux-amd64 linux-arm64 clean

run:
	go run ./cmd/server

test:
	go test ./...

build:
	mkdir -p dist
	go build -trimpath -o dist/classing-backend ./cmd/server

linux-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/classing-backend-linux-amd64 ./cmd/server

linux-arm64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/classing-backend-linux-arm64 ./cmd/server

clean:
	rm -rf dist
