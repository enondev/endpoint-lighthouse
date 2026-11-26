FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -o /bin/engine ./cmd/engine

FROM alpine:3.21
COPY --from=builder /bin/engine /bin/engine
EXPOSE 8081
ENTRYPOINT ["/bin/engine"]
