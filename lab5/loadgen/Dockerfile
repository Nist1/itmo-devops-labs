FROM golang:1.27-alpine AS build
WORKDIR /src
# Только стандартная библиотека: go.sum нет, go mod download не нужен
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app .

# distroless:nonroot — no shell, UID 65532: passes runAsNonRoot-style policies
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]
