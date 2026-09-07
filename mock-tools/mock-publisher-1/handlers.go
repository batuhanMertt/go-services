package main

import (
	"net/http"
	"time"

	"github.com/mindmons/integration-hub/internal/httpx"
)

type triggerRequest struct {
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
	// EventID doluysa aynı id ile gönderilir — idempotency'yi canlı göstermek için.
	EventID string `json:"event_id"`
}

type autoRequest struct {
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	creds := s.credentials()

	s.mu.RLock()
	auto := s.auto
	interval := s.autoInterval
	sent := len(s.recent)
	s.mu.RUnlock()

	registered := creds != nil
	keyPrefix := ""
	toolSlug := ""
	if registered {
		keyPrefix = creds.KeyPrefix
		toolSlug = creds.ToolSlug
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"registered":     registered,
		"tool_slug":      toolSlug,
		"key_prefix":     keyPrefix, // plaintext anahtar ASLA döndürülmez
		"auto_fire":      auto,
		"interval":       interval.String(),
		"ingest_url":     s.cfg.IngestURL,
		"event_types":    supportedTypes,
		"recent_records": sent,
	})
}

func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	var req triggerRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if req.EventType == "" {
		req.EventType = randomEventType()
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 100 {
		req.Count = 100 // mock tool bir yük test aracı değil
	}

	if s.credentials() == nil {
		httpx.Error(w, r, httpx.Internal("platforma henüz kaydolunmadı, birkaç saniye sonra deneyin", nil))
		return
	}

	results := make([]SendRecord, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		var forced *string
		if req.EventID != "" {
			forced = &req.EventID
		}

		rec, err := s.sendEvent(r.Context(), req.EventType, forced)
		if rec != nil {
			results = append(results, *rec)
		}
		if err != nil {
			s.log.WarnContext(r.Context(), "event gönderilemedi", "err", err.Error())
		}
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"sent":    len(results),
		"results": results,
	})
}

func (s *Server) handleAuto(w http.ResponseWriter, r *http.Request) {
	var req autoRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	interval := s.cfg.AutoInterval
	if req.Interval != "" {
		d, err := time.ParseDuration(req.Interval)
		if err != nil || d < time.Second || d > time.Hour {
			httpx.Error(w, r, httpx.BadRequest("invalid_interval",
				"interval 1s ile 1h arasında olmalı (örn. 5s)"))
			return
		}
		interval = d
	}

	s.mu.Lock()
	s.auto = req.Enabled
	s.autoInterval = interval
	s.mu.Unlock()

	s.log.Info("otomatik gönderim ayarlandı",
		"enabled", req.Enabled, "interval", interval.String())

	httpx.JSON(w, http.StatusOK, map[string]any{
		"auto_fire": req.Enabled,
		"interval":  interval.String(),
	})
}

func (s *Server) handleRecent(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	out := make([]SendRecord, len(s.recent))
	copy(out, s.recent)
	s.mu.RUnlock()

	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}
