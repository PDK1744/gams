BINARY_NAME=gams
VERSION ?= 0.1.0
LDFLAGS := -X 'main.Version=$(VERSION)' -s -w

.PHONY: all build test lint run clean

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/gams

test:
	go test -v -race ./...

lint:
	go vet ./...

run: build
	./$(BINARY_NAME) serve --config gams.toml

clean:
	rm -f $(BINARY_NAME) coverage.out *.db *.db-wal *.db-shm
