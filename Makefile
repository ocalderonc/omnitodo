.PHONY: run test build seed fmt vet

# Local only: no login, and cookies without Secure so plain http works.
run:
	OMNITODO_INSECURE_NO_AUTH=1 OMNITODO_COOKIE_SECURE=false go run ./cmd/omnitodo

test:
	go test ./...

seed:
	go run ./cmd/omnitodo seed

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/omnitodo ./cmd/omnitodo

fmt:
	gofmt -w .

vet:
	go vet ./...
