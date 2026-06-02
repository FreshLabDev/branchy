# SPDX-License-Identifier: Apache-2.0
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/branchy ./cmd/branchy

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
