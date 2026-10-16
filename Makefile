.PHONY: test build run vet frontend frontend-dev

test:
	go test -tags sqlite_fts5 ./...
	npm --prefix frontend ci
	npm --prefix frontend run typecheck

frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

frontend-dev:
	npm --prefix frontend run dev

build: frontend
	go build -tags sqlite_fts5 -o ./qagent ./cmd/qagent

run:
	go run -tags sqlite_fts5 ./cmd/qagent serve

vet:
	go vet -tags sqlite_fts5 ./...
