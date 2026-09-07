# 06 — Kafka Topic Tasarımı

## 1. Topic'ler

| Topic | Producer | Consumer | Partition | Retention | Mesaj |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `events.ingested` | event-ingest (outbox poller) | routing-engine | 6 | 7 gün | Doğrulanmış ham event + platform metadata |
| `events.delivery` | routing-engine, delivery-worker (retry), dlq-handler (replay) | delivery-worker | 6 | 7 gün | Hedefe özel teslimat görevi |
| `events.dlq` | delivery-worker | dlq-handler | 6 | 30 gün | Kalıcı olarak teslim edilemeyen mesaj |

**Doğrulandı:** Kafka UI (`:8085`) ve `kafka-topics.sh --describe` çıktısında
üç topic de 6 partition, replication factor 1 ile görünüyor.

---

## 2. Neden 3 topic?

### Neden tek topic değil?

Tek topic + mesaj içinde `type` alanı yaklaşımı şu sorunları doğururdu:

- Her consumer, ilgilenmediği mesajları da okuyup atmak zorunda kalır
  (routing-engine, teslimat görevlerini de tüketirdi — ~2× gereksiz I/O).
- Retention politikaları farklıdır: DLQ 30 gün, diğerleri 7 gün. Tek topic'te
  en uzun süre herkese uygulanırdı.
- Consumer lag metriği anlamsızlaşırdı: hangi aşamanın geride kaldığı
  ayırt edilemezdi.

### Neden daha fazla topic değil?

Event tipi başına topic (`events.user.created`, `events.document.generated` …)
yaklaşımı cazip görünür ama:

- **Yeni event tipi eklemek topic oluşturmayı gerektirirdi.** Bu, projenin
  temel hedefiyle ("admin panelinden tanımla, kod değiştirme") çelişir.
- Topic sayısı arttıkça broker metadata yükü ve rebalance süresi büyür.
- Routing engine tüm tiplere abone olmak zorunda kalırdı — dinamik topic
  aboneliği ek karmaşıklık demektir.

**Sonuç:** Topic'ler **boru hattı aşamalarına** göre ayrıldı, veri tipine göre
değil. Bu, her aşamanın bağımsız ölçeklenmesini ve izlenmesini sağlar.

---

## 3. Partition sayısı: 6

### Nasıl karar verildi?

Partition sayısı, bir consumer group'taki **etkin consumer sayısının üst
sınırıdır.** 6 partition = en fazla 6 paralel routing-engine instance'ı.
7. instance boşta bekler.

| Kriter | Değerlendirme |
| :--- | :--- |
| Beklenen yük | Bu ölçekte 1-2 instance yeterli |
| Büyüme payı | 6, 3× büyüme alanı bırakır |
| Rebalance maliyeti | Partition sayısı arttıkça rebalance yavaşlar |
| Tek broker | Yüzlerce partition tek broker'da anlamsız |
| Ölçek çarpanı | 12 CPU'lu bir makinede 6 tüketici mantıklı üst sınır |

**Kritik kısıt:** Kafka'da partition sayısı **artırılabilir ama azaltılamaz.**
Ayrıca artırma, mevcut anahtarların partition dağılımını bozar (hash modulo
değişir) ve o anahtarlar için sıralama garantisi kırılır. Bu yüzden başlangıç
değeri cömert seçildi.

`KAFKA_PARTITIONS` ile yapılandırılabilir.

---

## 4. Partition key ve sıralama garantisi

Kafka **yalnızca partition içinde** sıralama garanti eder. Doğru anahtar
seçimi bu yüzden kritiktir.

| Topic | Partition key | Neden? |
| :--- | :--- | :--- |
| `events.ingested` | `event_id` | Aynı event'in tüm mesajları (mükerrer yayın dahil) aynı partition'a düşer |
| `events.delivery` | `delivery_task_id` | Aynı görevin retry ve replay'leri sıralı işlenir |
| `events.dlq` | `delivery_task_id` | Aynı görevin DLQ kayıtları sıralı işlenir |

Balancer olarak `kafka.Hash{}` kullanılır → `partition = hash(key) % n`.

### Neden `source_tool` ile anahtarlamadık?

Tek bir tool'un tüm event'leri aynı partition'a düşerdi. Yoğun bir publisher
tek partition'ı doldurup **sıcak nokta (hot partition)** yaratır; diğer
partition'lar boşta beklerken o partition'ın consumer'ı tıkanırdı.

`event_id` UUID v4 olduğu için dağılım tekdüzedir.

### Sıralamaya gerçekten ihtiyaç var mı?

Kısmen. Platform **event'ler arası** sıralama garanti etmez ve etmemelidir —
bu, ölçeklenebilirliği yok ederdi. Garanti edilen: *aynı teslimat görevinin*
denemeleri sırayla işlenir, böylece 3. deneme 2.'den önce işlenmez.

---

## 5. Retention politikası

| Topic | `retention.ms` | Gerekçe |
| :--- | :--- | :--- |
| `events.ingested` | 7 gün | Bir hata fark edilip düzeltildikten sonra replay penceresi |
| `events.delivery` | 7 gün | Aynı |
| `events.dlq` | 30 gün | Operatörün inceleyip karar vermesi için daha uzun süre |

`cleanup.policy=delete` — compaction **kullanılmıyor.**

**Neden compaction değil?** Log compaction "her anahtarın son değerini sakla"
demektir; bu bir **durum (state)** deseni. Bizim mesajlarımız durum değil,
**olaydır**: aynı event'in 3. denemesi 1. denemenin yerini almaz, ikisi de
tarihin parçasıdır. Compaction burada veri kaybı olurdu.

---

## 6. Consumer group stratejisi

| Group | Topic | Servis |
| :--- | :--- | :--- |
| `routing-engine` | `events.ingested` | routing-engine |
| `delivery-worker` | `events.delivery` | delivery-worker |
| `dlq-handler` | `events.dlq` | dlq-handler |

Her servisin kendi group'u vardır → her mesajı **her servis bir kez** alır.
Aynı group içindeki instance'lar partition'ları paylaşır → yatay ölçekleme.

**Doğrulandı:** `--scale routing-engine=2` ile 6 partition iki instance
arasında **3/3** paylaşıldı.

### Offset yönetimi: elle commit

`CommitInterval: 0` — otomatik commit **kapalı**.

```go
msg := reader.FetchMessage(ctx)   // oku
process(msg)                       // işle
reader.CommitMessages(ctx, msg)    // ANCAK BAŞARILIYSA commit et
```

Otomatik commit, mesaj işlenirken çökme durumunda **veri kaybına
(at-most-once)** yol açardı: offset ilerlemiş ama iş yapılmamış olurdu.
Elle commit at-least-once verir; mükerrerlik downstream'de idempotency ile
ele alınır.

### Consumer ayarları ve gerekçeleri

| Ayar | Değer | Gerekçe |
| :--- | :--- | :--- |
| `StartOffset` | `FirstOffset` | Yeni group ilk bağlandığında baştaki mesajlar kaybolmasın |
| `SessionTimeout` | 30 sn | Yavaş bir consumer gruptan hemen atılmasın |
| `RebalanceTimeout` | 30 sn | İşlenmekte olan mesaj tamamlanabilsin |
| `HeartbeatInterval` | 3 sn | Session timeout'un ~1/10'u (Kafka önerisi) |
| `MaxWait` | 500 ms | Düşük gecikme ile boş döngü arasında denge |

### Producer ayarları

| Ayar | Değer | Gerekçe |
| :--- | :--- | :--- |
| `RequiredAcks` | `RequireAll` | Tüm in-sync replikalara yazılmadan başarılı sayılmaz |
| `Async` | `false` | Senkron: hata görülmeden "başarılı" denmez |
| `AllowAutoTopicCreation` | `false` | Yazım hatası yanlış partition sayısıyla topic yaratmasın |
| `Compression` | Snappy | Düşük CPU maliyetiyle ~%50 boyut kazancı |
| `MaxAttempts` | 5 | Geçici broker hatalarında yeniden dene |

---

## 7. DLQ tasarımı

### Neden ayrı bir topic, doğrudan DB değil?

Delivery worker DLQ kaydını doğrudan Postgres'e de yazabilirdi. Kafka
üzerinden gitmenin faydaları:

1. **Sorumluluk ayrımı:** delivery-worker'ın `dlq_events` tablosuna yazma
   yetkisi olmaz (least privilege — grants'te doğrulanabilir).
2. **Tampon:** DB geçici olarak yavaşsa DLQ mesajları Kafka'da bekler,
   teslimat akışını yavaşlatmaz.
3. **Genişletilebilirlik:** İleride bir alerting servisi aynı topic'e ikinci
   consumer group ile bağlanabilir — kod değişikliği gerekmez.

### Replay akışı

```
Admin API  →  dlq_events.status = 'replay_requested'   (yalnızca NİYET kaydedilir)
                        ↓
dlq-handler →  kuyruğu tarar, delivery_tasks'ı sıfırlar
            →  events.delivery topic'ine YENİDEN yayınlar
            →  dlq_events.status = 'replayed'
```

**Neden admin-api doğrudan Kafka'ya basmıyor?**
- Kontrol düzlemi, veri düzleminin (broker) bağımlılığını taşımaz.
- Replay talebi **kalıcıdır**: servis yeniden başlasa bile kaybolmaz.
- Kafka geçici olarak erişilemezse talep kuyrukta bekler.

Replay'de `attempt_count = 0` yapılır → mesaj **tam bir retry bütçesi** alır.

**Doğrulandı:** DLQ kaydı → replay → 1 denemede başarılı teslimat.

---

## 8. Gecikmeli retry — neden delay topic'leri YOK?

Kafka'da yerleşik gecikmeli mesaj yoktur. Yaygın çözüm "delay topic zinciri"dir
(`retry.1s`, `retry.2s`, `retry.4s` …). Bu proje **DB tabanlı gecikmeli
kuyruk** kullanır.

| Yaklaşım | Artı | Eksi |
| :--- | :--- | :--- |
| Delay topic zinciri | Kafka-native | Topic sayısı şişer, her gecikme için ayrı consumer, gecikme çözünürlüğü topic sayısıyla sınırlı |
| Consumer'da `sleep` | Basit | **Partition'ı bloke eder** — aynı partition'daki sağlıklı mesajlar da bekler. Kabul edilemez. |
| **DB'de zamanlanmış görev** | Keyfi gecikme, admin panelinde görünür, worker yeniden başlasa da plan kaybolmaz | Periyodik tarama maliyeti (partial index ile ihmal edilebilir) |

Seçilen: **DB tabanlı.** `delivery_tasks.next_attempt_at` alanı ve
`idx_tasks_due` partial index'i bu deseni O(log n) maliyetle çalıştırır.

Ek fayda: bu zamanlayıcı aynı zamanda bir **kurtarma mekanizmasıdır** —
routing-engine görevi DB'ye yazıp Kafka'ya basamadan çökerse, görev burada
bulunup yayınlanır.

---

## 9. KRaft modu (Zookeeper yok)

| Kriter | KRaft | Zookeeper |
| :--- | :--- | :--- |
| Proses sayısı | 1 | 2 |
| Bellek | ~400 MB | ~700 MB |
| Operasyonel yük | Tek sistem | İki ayrı sistem, iki ayrı failure mode |
| Gelecek | Kafka 4.0'da **tek seçenek** | Kafka 4.0'da **kaldırıldı** |

2026'da yeni bir kurulumda Zookeeper seçmek bilinçli teknik borç olurdu.

`CLUSTER_ID` sabit verilir: her `docker compose up` yeni bir küme kimliği
üretseydi, mevcut veri dizini tanınmaz hâle gelirdi.

---

## 10. Topic bootstrap

`KAFKA_AUTO_CREATE_TOPICS_ENABLE=false`.

Otomatik topic oluşturma açık olsaydı, koddaki bir yazım hatası
(`events.deliverry`) sessizce **varsayılan partition sayısıyla** yeni bir
topic yaratır ve mesajlar hiç tüketilmeyen bir kuyruğa akardı.

Topic'ler `kafka-init` container'ı tarafından açılışta oluşturulur
(`infra/kafka/create-topics.sh`). Script `--if-not-exists` kullanır →
idempotenttir, her `up` komutunda güvenle çalışır.

---

## 11. İzleme

`kafka-exporter` aşağıdaki metrikleri Prometheus'a taşır:

| Metrik | Kullanım |
| :--- | :--- |
| `kafka_consumergroup_lag` | **En kritik metrik.** Bir aşama geride mi kalıyor? |
| `kafka_topic_partition_current_offset` | Üretim hızı |
| `kafka_brokers` | Broker ayakta mı? |

**Alert:** `KafkaConsumerLagHigh` — 5 dakika boyunca lag > 1000.

Partition başına lag dağılımı Grafana'da ayrıca gösterilir: dengesiz dağılım
kötü partition key seçimine işaret eder.
