FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go *.sql ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o zomboid-discord-bot .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /build/zomboid-discord-bot /zomboid-discord-bot
ENTRYPOINT ["/zomboid-discord-bot"]
