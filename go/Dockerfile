# litSearch, Go implementation: one static binary (every pipeline step is Go
# code) on Alpine with poppler-utils for PDF -> text / Markdown.
#   go/run.sh build
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /litSearch .

FROM alpine:3.20
RUN apk add --no-cache poppler-utils ca-certificates tzdata
COPY --from=build /litSearch /usr/local/bin/litSearch
ENV LITSEARCH_ROOT=/work AGING_RESEARCH=/research LITSEARCH_IMPL=go
WORKDIR /work
