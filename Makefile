# =============================================================================
# Integration & Event Hub — geliştirici kısayolları
# Windows'ta `make` yoksa README'deki eşdeğer docker compose komutlarını kullanın.
# =============================================================================

SHELL := /bin/bash
COMPOSE := docker compose
GO_IMAGE := golang:1.26.8-alpine
GO_RUN := docker run --rm -v "$(CURDIR)":/src -v go-mod-cache:/go/pkg/mod -w /src -e GOFLAGS=-mod=mod $(GO_IMAGE)

# Go paketleri AÇIKÇA listelenir. `./...` kullanılsaydı admin-ui/node_modules
# içindeki üçüncü taraf Go dosyaları da taranırdı (Go bu dizini otomatik
# hariç tutmaz).
GO_PKGS := ./internal/... ./services/... ./mock-tools/... ./tools/...

.DEFAULT_GOAL := help

.PHONY: help
help: ## Bu yardım metnini göster
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# --- Kurulum ------------------------------------------------------------------

.PHONY: env
env: ## .env dosyasını güçlü secret'larla üret
	@./scripts/gen-secrets.sh

.PHONY: up
up: ## Tüm yığını ayağa kaldır
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Container'ları durdur (veri korunur)
	$(COMPOSE) down

.PHONY: clean
clean: ## Container ve TÜM VERİYİ sil
	$(COMPOSE) down -v --remove-orphans

.PHONY: restart
restart: ## Yığını yeniden başlat
	$(COMPOSE) restart

.PHONY: ps
ps: ## Servis durumlarını listele
	$(COMPOSE) ps

.PHONY: logs
logs: ## Tüm logları takip et
	$(COMPOSE) logs -f --tail=100

.PHONY: logs-pipeline
logs-pipeline: ## Yalnızca veri düzlemi loglarını takip et
	$(COMPOSE) logs -f --tail=100 event-ingest routing-engine delivery-worker dlq-handler

.PHONY: stats
stats: ## Kaynak kullanımını göster
	docker stats --no-stream

# --- Ölçekleme ----------------------------------------------------------------

.PHONY: scale
scale: ## Consumer'ları 2 instance'a çıkar (partition paylaşımını gözlemle)
	$(COMPOSE) up -d --scale routing-engine=2 --scale delivery-worker=2

.PHONY: unscale
unscale: ## Consumer'ları tek instance'a döndür
	$(COMPOSE) up -d --scale routing-engine=1 --scale delivery-worker=1

# --- Go ------------------------------------------------------------------------

.PHONY: build
build: ## Go kaynaklarını derle
	$(GO_RUN) go build $(GO_PKGS)

.PHONY: test
test: ## Go testlerini kapsam raporuyla çalıştır
	$(GO_RUN) sh -c "go test $(GO_PKGS) -race -coverprofile=coverage.out -covermode=atomic && go tool cover -func=coverage.out | tail -25"

.PHONY: vet
vet: ## go vet
	$(GO_RUN) go vet $(GO_PKGS)

.PHONY: lint
lint: ## golangci-lint
	docker run --rm -v "$(CURDIR)":/src -v go-mod-cache:/go/pkg/mod -w /src \
		golangci/golangci-lint:v1.62.2 golangci-lint run --timeout 5m

.PHONY: vuln
vuln: ## govulncheck ile bağımlılık taraması
	$(GO_RUN) sh -c "go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck $(GO_PKGS)"

.PHONY: tidy
tidy: ## go mod tidy
	$(GO_RUN) go mod tidy

# --- Frontend -------------------------------------------------------------------

.PHONY: ui-install
ui-install: ## Admin UI bağımlılıklarını kur
	cd admin-ui && npm ci

.PHONY: ui-build
ui-build: ## Admin UI'ı derle
	cd admin-ui && npm run build

.PHONY: ui-lint
ui-lint: ## Admin UI lint
	cd admin-ui && npm run lint

# --- Test / güvenlik --------------------------------------------------------------

.PHONY: e2e
e2e: ## Uçtan uca senaryoyu çalıştır
	./tests/e2e/e2e.sh

.PHONY: load
load: ## k6 yük testi (ingest)
	docker run --rm --network integration-hub-net -v "$(CURDIR)/tests/load":/scripts \
		-e INGEST_URL=http://event-ingest:8081/api/v1/events \
		grafana/k6:0.55.0 run /scripts/k6-ingest.js

.PHONY: trivy
trivy: ## Üretilen image'ları güvenlik taramasından geçir
	@for img in admin-api event-ingest routing-engine delivery-worker dlq-handler admin-ui; do \
		echo "--- integration-hub/$$img:1.0.0 ---"; \
		docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
			aquasec/trivy:0.58.1 image --severity HIGH,CRITICAL --exit-code 0 \
			integration-hub/$$img:1.0.0 || true; \
	done

# --- Veritabanı ------------------------------------------------------------------

.PHONY: psql
psql: ## Veritabanına psql ile bağlan
	$(COMPOSE) exec postgres psql -U $$(grep '^POSTGRES_USER=' .env | cut -d= -f2) -d $$(grep '^POSTGRES_DB=' .env | cut -d= -f2)

.PHONY: migrate-down
migrate-down: ## Son migration'ı geri al
	$(COMPOSE) run --rm migrate -path=/migrations \
		-database="postgres://$$(grep '^POSTGRES_USER=' .env | cut -d= -f2):$$(grep '^POSTGRES_PASSWORD=' .env | cut -d= -f2)@postgres:5432/$$(grep '^POSTGRES_DB=' .env | cut -d= -f2)?sslmode=disable" down 1

.PHONY: topics
topics: ## Kafka topic'lerini listele
	$(COMPOSE) exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe

.PHONY: lag
lag: ## Consumer group lag'lerini göster
	$(COMPOSE) exec kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --all-groups --describe
