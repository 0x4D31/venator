# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25.12
ARG ALPINE_VERSION=3.23

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.2.0

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY connector ./connector
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/venator .

FROM alpine:${ALPINE_VERSION} AS runtime-files
RUN apk add --no-cache ca-certificates tzdata

FROM scratch

ARG VERSION=0.2.0
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="Venator" \
      org.opencontainers.image.description="Scheduler-neutral batch detection engine" \
      org.opencontainers.image.source="https://github.com/0x4D31/venator" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.licenses="MIT"

COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /out/venator /venator

USER 65532:65532
ENTRYPOINT ["/venator"]
