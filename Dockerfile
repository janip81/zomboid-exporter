FROM golang:1.24-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o zomboid-exporter .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /build/zomboid-exporter /zomboid-exporter
EXPOSE 9091
ENTRYPOINT ["/zomboid-exporter"]
