# Stage 1: Build static Go binary
FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /prober ./cmd/prober

# Stage 2: Minimal distroless runtime (~10-15MB total RAM footprint)
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /prober /prober

ENV PORT=8080
ENV GOGC=50
ENV GOMEMLIMIT=16MiB
EXPOSE 8080

ENTRYPOINT ["/prober"]