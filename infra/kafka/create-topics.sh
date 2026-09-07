#!/bin/bash
# =============================================================================
# Kafka topic bootstrap.
# `auto.create.topics.enable=false` olduğu için topic'ler AÇIKÇA burada
# oluşturulur (production'da doğru yaklaşım: partition/retention kontrolü
# tesadüfe bırakılmaz). Script idempotent'tir; her açılışta güvenle koşar.
# =============================================================================
set -euo pipefail

BROKER="${KAFKA_BROKERS:-kafka:9092}"
PARTITIONS="${KAFKA_PARTITIONS:-6}"
RF="${KAFKA_REPLICATION_FACTOR:-1}"
RETENTION="${KAFKA_RETENTION_MS:-604800000}"
DLQ_RETENTION="${KAFKA_DLQ_RETENTION_MS:-2592000000}"

TOPIC_INGESTED="${KAFKA_TOPIC_INGESTED:-events.ingested}"
TOPIC_DELIVERY="${KAFKA_TOPIC_DELIVERY:-events.delivery}"
TOPIC_DLQ="${KAFKA_TOPIC_DLQ:-events.dlq}"

echo "Broker bekleniyor: ${BROKER}"
for i in $(seq 1 60); do
  if /opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server "$BROKER" >/dev/null 2>&1; then
    echo "Broker hazır."
    break
  fi
  if [ "$i" -eq 60 ]; then
    echo "HATA: broker 60 denemede hazır olmadı." >&2
    exit 1
  fi
  sleep 2
done

create_topic() {
  local name="$1" partitions="$2" retention="$3"
  echo "topic: ${name} (partitions=${partitions}, rf=${RF}, retention.ms=${retention})"
  /opt/kafka/bin/kafka-topics.sh \
    --bootstrap-server "$BROKER" \
    --create --if-not-exists \
    --topic "$name" \
    --partitions "$partitions" \
    --replication-factor "$RF" \
    --config "retention.ms=${retention}" \
    --config "cleanup.policy=delete" \
    --config "min.insync.replicas=1"
}

create_topic "$TOPIC_INGESTED" "$PARTITIONS" "$RETENTION"
create_topic "$TOPIC_DELIVERY" "$PARTITIONS" "$RETENTION"
# DLQ daha uzun süre saklanır: operatörün inceleyip replay etmesi için zaman gerekir.
create_topic "$TOPIC_DLQ" "$PARTITIONS" "$DLQ_RETENTION"

echo "--- Mevcut topic'ler ---"
/opt/kafka/bin/kafka-topics.sh --bootstrap-server "$BROKER" --list
echo "Kafka topic bootstrap tamamlandı."
