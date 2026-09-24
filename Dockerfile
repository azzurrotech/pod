FROM golang:1.20-alpine

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .

# Build the standalone server binary (library lives at the module root).
RUN go build -o pod-server ./cmd/pod

EXPOSE 8080

VOLUME ["/app/data"]

CMD ["/app/pod-server", "-port=8080", "-db=/app/data", "-mount=/"]