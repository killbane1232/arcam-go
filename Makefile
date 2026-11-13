.PHONY: run build test check
run:
	go run ./cmd/arcam
build:
	mkdir -p bin
	go build -trimpath -o bin/arcam ./cmd/arcam
test:
	go test -race ./...
check:
	go vet ./...
	go test -race ./...
