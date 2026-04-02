FROM alpine:latest

RUN apk add --no-cache ca-certificates iproute2

WORKDIR /app
COPY UDPlex /app/
COPY h5 /app/h5/

ENTRYPOINT ["/app/UDPlex", "-c", "/app/config.yaml"]
