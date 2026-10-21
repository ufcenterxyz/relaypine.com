.PHONY: setup infra gateway console test test-integration check build
setup:
	python3 scripts/setup.py
	cd apps/console && yarn install --frozen-lockfile
	cd services/gateway && go mod download
infra:
	docker compose up -d --wait mysql redis
gateway:
	./scripts/gateway.sh
console:
	cd apps/console && yarn dev --port 4810
test:
	cd services/gateway && go test -race -p 1 ./...
test-integration:
	./scripts/test-integration.sh
check:
	cd services/gateway && go vet ./...
	cd apps/console && yarn typecheck && yarn lint
build:
	cd services/gateway && go build ./cmd/gateway
	cd apps/console && yarn build
