# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
# modernc.org/sqlite is pure Go, so a fully static binary is possible.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/meteocomp ./cmd/meteocomp
RUN mkdir -p /out/data

# distroless/static ships CA certificates, tzdata and a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/meteocomp /usr/local/bin/meteocomp
COPY docker/config.yaml /etc/meteocomp/config.yaml
COPY --from=build --chown=nonroot:nonroot /out/data /data

# Settings (locations, API keys, poll interval) are edited in the web UI and stored
# in the database. docker/config.yaml is an optional env-driven seed used for
# automated tests: enable it with -e METEOCOMP_CONFIG=/etc/meteocomp/config.yaml.
#
# OWM_API_KEY / WEATHER_API_KEY: when set (non-empty), they provide the API key
# of the OpenWeatherMap / WeatherAPI.com providers and override any key typed in
# the web UI. When empty, keys are entered in the web UI.
ENV METEOCOMP_HTTP_ADDR=:8080 \
    METEOCOMP_DB_PATH=/data/meteocomp.db \
    OWM_API_KEY="" \
    WEATHER_API_KEY=""

VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/meteocomp"]
