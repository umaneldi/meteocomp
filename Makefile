build:
	go build -o bin/meteocomp ./cmd/meteocomp

test:
	go vet ./...
	go test ./...

run:
	go run ./cmd/meteocomp

.PHONY: build test run
