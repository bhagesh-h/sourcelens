# litsearch in a container: the Go binary plus poppler-utils for PDF conversion.
#
#   docker build -t litsearch .
#   docker run --rm -v "$HOME/litsearch:/data" -e LITSEARCH_EMAIL=you@example.org \
#       litsearch "CRISPR base editing"
#
# Catalogues are written to /data (mount a host folder there).
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /litsearch ./cmd/litsearch

FROM alpine:3.20
RUN apk add --no-cache poppler-utils ca-certificates tzdata
COPY --from=build /litsearch /usr/local/bin/litsearch
ENV LITSEARCH_OUTPUT=/data LITSEARCH_SETTINGS=/data/.litsearch-settings.yaml
WORKDIR /data
ENTRYPOINT ["litsearch"]
CMD ["help"]
