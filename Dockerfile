# SPDX-License-Identifier: Apache-2.0
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# Stamp build metadata into the binary so logs and /healthz report the exact
# release. Defaults keep a plain build identifiable as "dev".
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/branchy ./cmd/branchy

FROM alpine:3.22
# ca-certificates is required for outbound TLS to api.github.com and
# api.telegram.org from the static (CGO-disabled) binary.
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 branchy
WORKDIR /app
COPY --from=build /out/branchy /app/branchy
COPY migrations /app/migrations
USER branchy
EXPOSE 8080
ENTRYPOINT ["/app/branchy"]
