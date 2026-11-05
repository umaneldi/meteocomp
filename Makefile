VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

build:
	go build -ldflags="-X github.com/yanndethe/meteocomp/internal/version.Version=$(VERSION)" -o bin/meteocomp ./cmd/meteocomp

test:
	go vet ./...
	go test ./...

test-web:
	node --check internal/web/static/app.js
	node --test internal/web/forecast_test.cjs

run:
	go run ./cmd/meteocomp

.PHONY: build test test-web run
