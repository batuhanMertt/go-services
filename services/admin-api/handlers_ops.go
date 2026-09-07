package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mindmons/integration-hub/internal/httpx"
)

// --- Event akışı ---------------------------------------------------------------

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)

	f := EventFilter{
		EventType:  q(r, "event_type"),
		SourceTool: q(r, "source_tool"),
		Status:     q(r, "status"),
		Search:     q(r, "search"),
		Limit:      limit,
		Offset:     offset,
	}
	if v := q(r, "since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &t
		}
	}

	events, total, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("event listesi alınamadı", err))
		return
	}
	writeList(w, events, total, limit, offset)
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	ev, err := s.store.GetEvent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, notFoundOr(err, "event bulunamadı"))
		return
	}
	httpx.JSON(w, http.StatusOK, ev)
}

// handleEventStream, canlı event akışını Server-Sent Events ile yayınlar.
//
// Neden SSE, WebSocket değil? Akış TEK YÖNLÜDÜR (sunucu → tarayıcı). SSE düz
// HTTP üzerinde çalışır, otomatik yeniden bağlanır, proxy'lerle uyumludur ve
// tarayıcıda ek kütüphane gerektirmez. WebSocket iki yönlü iletişim için
// gereklidir; burada o ihtiyaç yok, ek karmaşıklık olurdu.
// (Görev tanımındaki "çift yönlü WebSocket streaming" kapsam dışıdır.)
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, r, httpx.Internal("streaming desteklenmiyor", nil))
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // nginx tamponlamasını kapat
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	cursor := time.Now().Add(-1 * time.Minute)

	ticker := time.NewTicker(s.cfg.SSEInterval)
	defer ticker.Stop()

	// Bağlantı kurulur kurulmaz bir "hazır" olayı gönder: istemci bağlantının
	// canlı olduğunu hemen anlar.
	_, _ = fmt.Fprintf(w, "event: ready\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-heartbeat.C:
			// Yorum satırı: proxy'lerin bağlantıyı zaman aşımına uğratmasını önler.
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()

		case <-ticker.C:
			events, err := s.store.EventsSince(ctx, cursor, 50)
			if err != nil {
				s.log.WarnContext(ctx, "sse sorgusu başarısız", "err", err.Error())
				continue
			}
			for _, ev := range events {
				if ev.ReceivedAt.After(cursor) {
					cursor = ev.ReceivedAt
				}
				payload, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "event: event\ndata: %s\n\n", payload)
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

// --- Teslimat denemeleri -----------------------------------------------------------

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)

	f := AttemptFilter{
		SubscriptionID: q(r, "subscription_id"),
		EventID:        q(r, "event_id"),
		Status:         q(r, "status"),
		OnlyFailed:     q(r, "only_failed") == "true",
		Limit:          limit,
		Offset:         offset,
	}

	attempts, total, err := s.store.ListAttempts(r.Context(), f)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("teslimat denemeleri alınamadı", err))
		return
	}
	writeList(w, attempts, total, limit, offset)
}

// --- DLQ -----------------------------------------------------------------------------

func (s *Server) handleListDLQ(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)

	f := DLQFilter{
		Status:         q(r, "status"),
		SubscriptionID: q(r, "subscription_id"),
		EventType:      q(r, "event_type"),
		Limit:          limit,
		Offset:         offset,
	}

	items, total, err := s.store.ListDLQ(r.Context(), f)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("dlq listesi alınamadı", err))
		return
	}
	writeList(w, items, total, limit, offset)
}

func (s *Server) handleGetDLQ(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetDLQEvent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, notFoundOr(err, "dlq kaydı bulunamadı"))
		return
	}
	httpx.JSON(w, http.StatusOK, item)
}

// handleReplayDLQ, tekil veya toplu replay talebi oluşturur (FR-5.2).
//
// Admin API yalnızca NİYETİ kaydeder (status = replay_requested); mesajı
// Kafka'ya dlq-handler basar. Böylece kontrol düzlemi broker bağımlılığı
// taşımaz ve talep, servis yeniden başlasa bile kaybolmaz.
func (s *Server) handleReplayDLQ(w http.ResponseWriter, r *http.Request) {
	var req ReplayRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	ctx := r.Context()
	resp := ReplayResponse{}

	switch {
	case len(req.IDs) > 0:
		ids, err := ValidateUUIDList(req.IDs)
		if err != nil {
			httpx.Error(w, r, httpx.BadRequest("invalid_ids", err.Error()))
			return
		}
		resp.Requested = len(ids)

		n, err := s.store.RequestReplayByIDs(ctx, ids)
		if err != nil {
			httpx.Error(w, r, httpx.Internal("replay talebi kaydedilemedi", err))
			return
		}
		resp.Replayed = n
		resp.Skipped = len(ids) - n

		s.audit(ctx, r, "dlq.replay", "dlq_event", "", nil,
			map[string]any{"ids": ids, "queued": n})

	case req.SubscriptionID != "":
		n, err := s.store.RequestReplayBySubscription(ctx, req.SubscriptionID, req.Limit)
		if err != nil {
			httpx.Error(w, r, httpx.Internal("toplu replay talebi kaydedilemedi", err))
			return
		}
		resp.Requested = n
		resp.Replayed = n

		s.audit(ctx, r, "dlq.replay_bulk", "subscription", req.SubscriptionID, nil,
			map[string]any{"queued": n, "limit": req.Limit})

	default:
		httpx.Error(w, r, httpx.BadRequest("missing_target",
			"ids veya subscription_id alanlarından biri zorunlu"))
		return
	}

	s.mReplayRequest.Inc()
	httpx.JSON(w, http.StatusAccepted, resp)
}

// handleDiscardDLQ, kayıtları kalıcı olarak "işlenmeyecek" işaretler.
func (s *Server) handleDiscardDLQ(w http.ResponseWriter, r *http.Request) {
	var req ReplayRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	ids, err := ValidateUUIDList(req.IDs)
	if err != nil {
		httpx.Error(w, r, httpx.BadRequest("invalid_ids", err.Error()))
		return
	}
	if len(ids) == 0 {
		httpx.Error(w, r, httpx.BadRequest("missing_ids", "ids zorunlu"))
		return
	}

	n, err := s.store.DiscardDLQ(r.Context(), ids)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("kayıtlar işaretlenemedi", err))
		return
	}

	s.audit(r.Context(), r, "dlq.discard", "dlq_event", "", nil,
		map[string]any{"ids": ids, "discarded": n})

	httpx.JSON(w, http.StatusOK, map[string]int{"discarded": n})
}

// --- Denetim ve istatistik ---------------------------------------------------------

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)

	logs, total, err := s.store.ListAuditLogs(r.Context(), AuditFilter{
		Action:       q(r, "action"),
		ResourceType: q(r, "resource_type"),
		ActorID:      q(r, "actor_id"),
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		httpx.Error(w, r, httpx.Internal("denetim kayıtları alınamadı", err))
		return
	}
	writeList(w, logs, total, limit, offset)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.DashboardStats(r.Context())
	if err != nil {
		httpx.Error(w, r, httpx.Internal("istatistikler alınamadı", err))
		return
	}
	httpx.JSON(w, http.StatusOK, stats)
}
