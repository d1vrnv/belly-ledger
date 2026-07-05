FROM golang:1.25 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o belly-ledger .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*
WORKDIR /app

COPY --from=builder /app/belly-ledger /app/belly-ledger

RUN mkdir -p /app/data
CMD ["/app/belly-ledger"]
