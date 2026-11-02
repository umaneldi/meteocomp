build:
	go build -o bin/meteocomp ./cmd/meteocomp

test:
	go vet ./...
	go test ./...

run:
	go run ./cmd/meteocomp -config config.yaml

.PHONY: build test run
