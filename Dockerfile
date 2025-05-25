FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o app .

FROM alpine:latest

RUN adduser -D -g '' appuser

WORKDIR /app
COPY --from=builder /app/app .

RUN chown -R appuser /app

USER appuser

EXPOSE 8080

ENTRYPOINT ["./app", "/app/config.toml"]