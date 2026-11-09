.PHONY: run test build fmt vet

run:
	go run ./cmd/omnitodo

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/omnitodo ./cmd/omnitodo

fmt:
	gofmt -w .

vet:
	go vet ./...
