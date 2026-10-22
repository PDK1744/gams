BINARY_NAME=gams
VERSION ?= 1.0.0
LDFLAGS := -X 'main.Version=$(VERSION)' -s -w

.PHONY: all build test test-all test-sdk test-sdk-ts test-sdk-py lint run docker-build clean

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/gams

test:
	go test -v -race ./...

test-all: test test-sdk

test-sdk: test-sdk-ts test-sdk-py

test-sdk-ts:
	cd sdks/client-ts && npm test

test-sdk-py: build
	python3 -m pytest -v sdks/client-py/tests

docker-build:
	docker build -t gams:$(VERSION) -t gams:latest .

lint:
	go vet ./...

run: build
	./$(BINARY_NAME) serve --config gams.toml

clean:
	rm -f $(BINARY_NAME) coverage.out *.db *.db-wal *.db-shm
