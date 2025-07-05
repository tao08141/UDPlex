FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY UDPlex /app/
COPY h5 /app/h5/

ENTRYPOINT ["/app/UDPlex", "-c", "/app/config.json"]