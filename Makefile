.PHONY: test build run vet

test:
	go test ./...

build:
	go build -o bin/qagent ./cmd/qagent

run:
	go run ./cmd/qagent serve

vet:
	go vet ./...
