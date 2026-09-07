package main

import (
	"net/http"

	"github.com/mindmons/integration-hub/internal/httpx"
)

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	total := len(s.received)
	s.mu.RUnlock()

	httpx.JSON(w, http.StatusOK, map[string]any{
		"registered":     s.registered.Load(),
		"tool_slug":      s.cfg.ToolSlug,
		"webhook_url":    s.cfg.PublicURL,
		"fail_every":     s.failEvery.Load(),
		"fail_status":    s.cfg.FailStatus,
		"processed":      s.counter.Load(),
		"recent_records": total,
	})
}

type failureRequest struct {
	// FailEvery: 0 = kapalı, 3 = her 3 istekten biri fail eder.
	FailEvery int `json:"fail_every"`
}

func (s *Server) handleFailureToggle(w http.ResponseWriter, r *http.Request) {
	var req failureRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.FailEvery < 0 || req.FailEvery > 1000 {
		httpx.Error(w, r, httpx.BadRequest("invalid_value",
			"fail_every 0 ile 1000 arasında olmalı (0 = kapalı)"))
		return
	}

	s.failEvery.Store(int64(req.FailEvery))
	s.counter.Store(0)

	s.log.Info("arıza simülasyonu güncellendi", "fail_every", req.FailEvery)

	httpx.JSON(w, http.StatusOK, map[string]any{
		"fail_every": req.FailEvery,
		"enabled":    req.FailEvery > 0,
	})
}

func (s *Server) handleReceived(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	out := make([]Received, len(s.received))
	copy(out, s.received)
	s.mu.RUnlock()

	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}
