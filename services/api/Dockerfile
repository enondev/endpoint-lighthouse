FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /api-go ./cmd/api

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /api-go /api-go
ENV PORT=8080
ENV GOGC=50
ENV GOMEMLIMIT=16MiB
EXPOSE 8080
ENTRYPOINT ["/api-go"]