.PHONY: test test-race build run vet frontend frontend-dev pdf-extractor-image doctor providers search-smoke runtime-build runtime-install runtime-start runtime-status

test:
	go test -tags sqlite_fts5 ./...
	npm --prefix frontend ci
	npm --prefix frontend run typecheck
	npm --prefix frontend test

test-race:
	CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...

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

providers:
	go run -tags sqlite_fts5 ./cmd/qagent providers --live --json

search-smoke:
	go run -tags sqlite_fts5 ./cmd/qagent search-smoke --json

runtime-build:
	go build -tags sqlite_fts5 -o ./qagent ./cmd/qagent

runtime-install: runtime-build
	./qagent runtime install --model-bin "$(MODEL_BIN)" --model-path "$(MODEL_PATH)" --device "$(or $(DEVICE),Vulkan1)" --context "$(or $(CONTEXT),32768)" $(if $(filter 1 true,$(ENABLE)),--enable,)

runtime-start:
	./qagent runtime start

runtime-status:
	./qagent runtime status
