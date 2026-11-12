# Build from the repo root:
#   docker build -f deploy/server/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/doxacast-server ./cmd/doxacast-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/doxacast-server /doxacast-server
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/doxacast-server"]
