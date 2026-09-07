# =============================================================================
# Tüm Go servisleri için ortak çok aşamalı (multi-stage) build.
#
# Tek Dockerfile kullanılmasının gerekçesi: 8 servisin build adımları birebir
# aynı. Sekiz kopya tutmak, birinde yapılan güvenlik düzeltmesinin diğerlerinde
# unutulması demektir. Hangi servisin derleneceği CMD_PATH build arg'ı ile
# belirlenir (docker-compose.yml'de her servis kendi yolunu verir).
#
# Güvenlik kararları:
#   - distroless/static: shell, paket yöneticisi, libc yok → saldırı yüzeyi ~0.
#     Trivy taramasında OS paketi CVE'si çıkmaz çünkü OS paketi yoktur.
#   - nonroot (uid 65532): container root olarak çalışmaz (NFR-3.3).
#   - CGO_ENABLED=0: tamamen statik ikili; distroless/static yeterli olur.
#   - Secret'lar build arg olarak GEÇİLMEZ; yalnızca runtime env'den okunur
#     (build arg'lar image katmanlarında ve `docker history`'de kalıcıdır).
# =============================================================================
# syntax=docker/dockerfile:1.7

FROM golang:1.23.4-alpine AS builder

# git: bazı modüllerin çekilmesi için; ca-certificates: TLS doğrulaması.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Bağımlılıklar önce kopyalanır: kaynak kodu değiştiğinde modül indirme
# katmanı önbellekten gelir (build süresi ~10 kat kısalır).
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Derlenecek servisin yolu, örn. services/admin-api
ARG CMD_PATH
ARG BUILD_VERSION=dev

RUN test -n "$CMD_PATH" || (echo "CMD_PATH build arg zorunlu" >&2 && exit 1) && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/service ./${CMD_PATH} && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./tools/healthcheck

# --- Çalışma zamanı image'ı --------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/service     /app/service
COPY --from=builder /out/healthcheck /app/healthcheck

# OpenAPI kontratı: admin-api /docs ve /openapi.yaml uçlarından sunar.
# Diğer servislerde kullanılmaz ama birkaç KB olduğu için ayrı image
# varyantı üretmeye değmez.
COPY --from=builder /src/docs/api-contracts /app/api-contracts
COPY --from=builder /src/docs/api-contracts/admin-api.openapi.yaml /app/openapi.yaml

USER nonroot:nonroot

ENTRYPOINT ["/app/service"]
