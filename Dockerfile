# sourcelens in a container: the Go binary plus poppler-utils for PDF conversion.
#
#   docker build -t sourcelens .
#   docker run --rm -v "$HOME/sourcelens:/data" -e SOURCELENS_EMAIL=you@example.org \
#       sourcelens "CRISPR base editing"
#
# Catalogues are written to /data (mount a host folder there).
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /sourcelens ./cmd/sourcelens

FROM alpine:3.20
RUN apk add --no-cache poppler-utils ca-certificates tzdata
COPY --from=build /sourcelens /usr/local/bin/sourcelens
ENV SOURCELENS_OUTPUT=/data SOURCELENS_SETTINGS=/data/.sourcelens-settings.yaml
WORKDIR /data
ENTRYPOINT ["sourcelens"]
CMD ["help"]
