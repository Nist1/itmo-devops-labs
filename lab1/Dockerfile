FROM golang:1.22 AS build

WORKDIR /app

COPY api/go.mod ./
COPY api/main.go ./

RUN CGO_ENABLED=0 go build -o /out/api .

EXPOSE 8080

ENTRYPOINT ["/out/api"]
