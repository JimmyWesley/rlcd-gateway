# Monorepo: frontend/ (React + Vite) builds into gateway/internal/web/dist,
# which gateway/ (Go) embeds into a single binary.
export PATH := $(PATH):/usr/local/go/bin

.PHONY: build ui gateway test dev-gateway dev-ui clean

build: ui gateway

ui:
	cd frontend && npm install --no-audit --no-fund && npm run build

gateway:
	cd gateway && go build -o ../bin/rlcd-gateway ./cmd/rlcd-gateway

test:
	cd gateway && go vet ./... && go test ./...
	cd frontend && npm run typecheck

# Two terminals: the gateway on :4777, and Vite on :5177 with hot reload.
dev-gateway:
	cd gateway && go run ./cmd/rlcd-gateway

dev-ui:
	cd frontend && npm run dev

clean:
	rm -rf bin gateway/internal/web/dist/assets gateway/internal/web/dist/index.html
