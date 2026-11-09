# syntax=docker/dockerfile:1
#
# Build context is the repository root (see docker-compose.prod.yml).
# One static binary on distroless: it serves the app, migrates the database at
# startup, and doubles as its own healthcheck and backup tool.

# --- build ------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so the layer is cached across source-only changes.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# CGO off: modernc.org/sqlite is pure Go, so the binary is fully static.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/omnitodo ./cmd/omnitodo

# The database directory, owned by distroless's nonroot user. A *named* volume
# mounted on /data copies this ownership when it is first created; without it
# the volume is root-owned and SQLite fails with "readonly database".
RUN mkdir -p /out/data

# --- runtime ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/omnitodo /usr/local/bin/omnitodo
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
ENV OMNITODO_ADDR=:8080 \
    OMNITODO_DB=/data/omnitodo.db
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/omnitodo"]
