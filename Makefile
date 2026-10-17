.PHONY: test build run vet frontend frontend-dev pdf-extractor-image doctor search-smoke

test:
	go test -tags sqlite_fts5 ./...
	npm --prefix frontend ci
	npm --prefix frontend run typecheck
	npm --prefix frontend test

frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

frontend-dev:
	npm --prefix frontend run dev

pdf-extractor-image:
	docker build --pull=false -t qagent-pdf-extractor:local -f internal/runtime/pdfextract/Dockerfile internal/runtime/pdfextract

build: frontend
	go build -tags sqlite_fts5 -o ./qagent ./cmd/qagent

run:
	go run -tags sqlite_fts5 ./cmd/qagent serve

vet:
	go vet -tags sqlite_fts5 ./...

doctor:
	go run -tags sqlite_fts5 ./cmd/qagent doctor --json

search-smoke:
	go run -tags sqlite_fts5 ./cmd/qagent search-smoke --json
