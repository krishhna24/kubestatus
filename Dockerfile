# Build stage
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /kubestatus ./cmd/kubestatus

# Runtime stage: static binary on a minimal, non-root image with no shell.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /kubestatus /kubestatus
USER nonroot:nonroot
ENTRYPOINT ["/kubestatus"]
