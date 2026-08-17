# syntax=docker/dockerfile:1.7
FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /out/clisynth ./cmd/clisynth

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/clisynth /usr/local/bin/clisynth
COPY examples/definitions /app/definitions
ENV OUTPUTFSM_ROOT=/app/definitions
ENV PORT=8080
ENV CLISYNTH_RELOAD_MAX_BODY_BYTES=33554432
ENV CLISYNTH_RELOAD_MAX_DEFINITIONS=10000
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/clisynth"]
CMD ["serve"]
