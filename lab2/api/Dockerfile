FROM golang:1.27 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /api .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /api /api

EXPOSE 8080

ENTRYPOINT ["/api"]
