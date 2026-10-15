BINARY := bin/wasmgate
PKG    := ./cmd/wasmgate

.PHONY: build run test race bench tidy lint clean

build:
	go build -o $(BINARY) $(PKG)

run: build
	$(BINARY) -config configs/wasmgate.yaml

test:
	go test ./...

race:
	go test -race ./...

bench:
	go test -run '^$$' -bench . -benchmem ./...

tidy:
	go mod tidy

lint:
	golangci-lint run

clean:
	go clean
	rm -rf bin
