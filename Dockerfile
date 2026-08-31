FROM golang:1.20-alpine

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o pod-server ./cmd

EXPOSE 8080

CMD ["/app/pod-server", "--port=8080", "--db=/app/data.db"]