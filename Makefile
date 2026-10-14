.PHONY: test build run vet frontend frontend-dev

test:
	go test ./...
	npm --prefix frontend ci
	npm --prefix frontend run typecheck

frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

frontend-dev:
	npm --prefix frontend run dev

build: frontend
	go build -o ./qagent ./cmd/qagent

run:
	go run ./cmd/qagent serve

vet:
	go vet ./...
