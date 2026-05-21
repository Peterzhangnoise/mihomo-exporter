FROM golang:1.24 AS builder

WORKDIR /app

COPY . .

RUN go mod tidy
RUN CGO_ENABLED=0 go build -o mihomo-exporter .

FROM alpine:latest

COPY --from=builder /app/mihomo-exporter /mihomo-exporter

EXPOSE 9988

ENTRYPOINT ["/mihomo-exporter"]
