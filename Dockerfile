FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY UDPlex /app/

ENTRYPOINT ["/app/UDPlex", "-c", "/app/config.json"]