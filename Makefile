BINARY_NAME=gams
VERSION ?= 0.1.0
LDFLAGS := -X 'main.Version=$(VERSION)' -s -w

.PHONY: all build test test-sdk test-sdk-ts test-sdk-py lint run clean

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/gams

test:
	go test -v -race ./...

test-sdk: test-sdk-ts test-sdk-py

test-sdk-ts:
	cd sdks/client-ts && npm test

test-sdk-py: build
	python3 -m pytest -v sdks/client-py/tests

lint:
	go vet ./...

run: build
	./$(BINARY_NAME) serve --config gams.toml

clean:
	rm -f $(BINARY_NAME) coverage.out *.db *.db-wal *.db-shm
