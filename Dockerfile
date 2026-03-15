FROM golang:1.25.6 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o external-dns-webhook-ngcloud .

FROM gcr.io/distroless/static
COPY --from=builder /app/external-dns-webhook-ngcloud /external-dns-webhook-ngcloud
ENTRYPOINT ["/external-dns-webhook-ngcloud"]
