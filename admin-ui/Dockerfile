# =============================================================================
# Admin UI — Vite build + nginx (unprivileged) ile statik sunum
# =============================================================================
# syntax=docker/dockerfile:1.7

FROM node:22.12-alpine AS builder

WORKDIR /app

# Bağımlılıklar önce: kaynak değiştiğinde npm ci katmanı önbellekten gelir.
COPY package.json package-lock.json ./
# `npm ci`, package-lock.json'a birebir uyar — `npm install`in aksine
# sürümleri sessizce yükseltmez (reproducible build).
RUN npm ci --no-audit --no-fund

COPY . .

# API taban yolu build zamanında gömülür (Vite statik değişken enjeksiyonu).
ARG VITE_API_BASE_URL=/api
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL

RUN npm run build

# --- Çalışma zamanı ----------------------------------------------------------
# nginx-unprivileged: root olmayan kullanıcı, 8080 portu (NFR-3.3).
FROM nginxinc/nginx-unprivileged:1.27-alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 8080

# Varsayılan nginx entrypoint kullanılır.
