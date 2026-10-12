ARG GO_VERSION
ARG SEMGREP_VERSION

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -o /out/runner .

# В образе semgrep уже есть и сам semgrep, и git; добавляем только наш бинарь и правила.
FROM semgrep/semgrep:${SEMGREP_VERSION}
COPY --from=build /out/runner /usr/local/bin/runner
COPY rules /rules
ENTRYPOINT ["/usr/local/bin/runner"]
