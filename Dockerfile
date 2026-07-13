# syntax=docker/dockerfile:1.7
FROM golang:1.24-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -o /out/classing-backend ./cmd/server \
	&& CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -o /out/classing-storage-audit ./cmd/storage-audit

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S classing \
    && adduser -S -G classing -h /app classing \
    && mkdir -p /data/releases \
    && chown -R classing:classing /data /app
WORKDIR /app
COPY --from=build /out/classing-backend /app/classing-backend
COPY --from=build /out/classing-storage-audit /app/classing-storage-audit
USER classing
EXPOSE 8080
ENV APP_ENV=production HTTP_ADDR=:8080
ENTRYPOINT ["/app/classing-backend"]
