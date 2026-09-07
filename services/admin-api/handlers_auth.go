package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mindmons/integration-hub/internal/auth"
	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/httpx"
)

// handleLogin, e-posta + şifre ile access/refresh token üretir.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		httpx.Error(w, r, httpx.BadRequest("missing_credentials", "e-posta ve şifre zorunlu"))
		return
	}

	ctx := r.Context()
	user, err := s.store.GetUserByEmail(ctx, req.Email)

	// Kullanıcı yok ile şifre yanlış AYNI yanıtı döner: aksi halde saldırgan
	// hangi e-postaların kayıtlı olduğunu keşfedebilir (user enumeration).
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Zamanlama farkını da kapatmak için sahte bir hash doğrulaması yapılır.
			_ = auth.VerifyPassword(req.Password, s.dummyHash())
			s.mAuthFailures.Inc("unknown_user")
			httpx.Error(w, r, httpx.Unauthorized("invalid_credentials", "e-posta veya şifre hatalı"))
			return
		}
		httpx.Error(w, r, httpx.Internal("kullanıcı okunamadı", err))
		return
	}

	if !user.IsActive {
		s.mAuthFailures.Inc("inactive_user")
		httpx.Error(w, r, httpx.Forbidden("account_disabled", "hesap devre dışı"))
		return
	}

	if err := auth.VerifyPassword(req.Password, user.passwordHash); err != nil {
		s.mAuthFailures.Inc("bad_password")
		s.audit(ctx, r, "auth.login_failed", "admin_user", user.ID, nil,
			map[string]string{"email": user.Email})
		httpx.Error(w, r, httpx.Unauthorized("invalid_credentials", "e-posta veya şifre hatalı"))
		return
	}

	resp, err := s.issueTokens(r, user)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := s.store.TouchLogin(ctx, user.ID); err != nil {
		s.log.WarnContext(ctx, "son giriş zamanı güncellenemedi", "err", err.Error())
	}
	s.audit(ctx, r, "auth.login", "admin_user", user.ID, nil, map[string]string{"email": user.Email})

	httpx.JSON(w, http.StatusOK, resp)
}

// dummyHash, kullanıcı bulunamadığında zamanlama saldırısını önlemek için
// doğrulanan gerçek bir argon2id hash'i ("timing-equalizer").
//
// Bu olmadan, var olmayan bir e-posta anında 401 dönerdi; var olan bir e-posta
// ise argon2 doğrulaması kadar (~80 ms) beklerdi. Saldırgan bu farkı ölçerek
// hangi e-postaların kayıtlı olduğunu tespit edebilirdi (user enumeration).
// Hash açılışta bir kez, rastgele bir parolayla üretilir.
func (s *Server) dummyHash() string {
	s.dummyOnce.Do(func() {
		secret, err := cryptox.RandomHex(16)
		if err != nil {
			secret = "fallback-timing-equalizer-secret"
		}
		h, err := auth.HashPassword(secret, s.argonParams)
		if err == nil {
			s.dummyHashValue = h
		}
	})
	return s.dummyHashValue
}

func (s *Server) issueTokens(r *http.Request, user *AdminUser) (*TokenResponse, error) {
	access, exp, err := s.tokens.GenerateAccess(user.ID, user.Email, user.Role)
	if err != nil {
		return nil, httpx.Internal("token üretilemedi", err)
	}

	refresh, err := s.tokens.GenerateRefresh()
	if err != nil {
		return nil, httpx.Internal("refresh token üretilemedi", err)
	}

	if err := s.store.StoreRefreshToken(r.Context(), user.ID, refresh.Hash,
		refresh.ExpiresAt, r.UserAgent(), httpx.ClientIP(r)); err != nil {
		return nil, httpx.Internal("refresh token saklanamadı", err)
	}

	safeUser := *user
	safeUser.passwordHash = ""

	return &TokenResponse{
		AccessToken:  access,
		RefreshToken: refresh.Raw,
		TokenType:    "Bearer",
		ExpiresAt:    exp,
		User:         safeUser,
	}, nil
}

// handleRefresh, refresh token'ı tüketip yeni token çifti üretir (rotation).
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		httpx.Error(w, r, httpx.BadRequest("missing_token", "refresh_token zorunlu"))
		return
	}

	ctx := r.Context()
	hash := auth.HashRefreshToken(req.RefreshToken)

	userID, err := s.store.ConsumeRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.mAuthFailures.Inc("refresh_invalid")
			httpx.Error(w, r, httpx.Unauthorized("invalid_refresh_token",
				"refresh token geçersiz, süresi dolmuş veya daha önce kullanılmış"))
			return
		}
		httpx.Error(w, r, httpx.Internal("refresh token doğrulanamadı", err))
		return
	}

	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		httpx.Error(w, r, httpx.Unauthorized("invalid_refresh_token", "kullanıcı bulunamadı"))
		return
	}
	if !user.IsActive {
		httpx.Error(w, r, httpx.Forbidden("account_disabled", "hesap devre dışı"))
		return
	}

	resp, err := s.issueTokens(r, user)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// handleLogout, kullanıcının tüm refresh token'larını iptal eder.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		httpx.Error(w, r, httpx.Unauthorized("unauthenticated", "oturum bulunamadı"))
		return
	}

	if err := s.store.RevokeAllRefreshTokens(r.Context(), user.UserID); err != nil {
		httpx.Error(w, r, httpx.Internal("oturum kapatılamadı", err))
		return
	}
	s.audit(r.Context(), r, "auth.logout", "admin_user", user.UserID, nil, nil)

	httpx.JSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"note":   "access token en fazla TTL süresi kadar geçerli kalır",
	})
}

// handleMe, oturum sahibinin profilini döner.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := userFrom(r.Context())
	if claims == nil {
		httpx.Error(w, r, httpx.Unauthorized("unauthenticated", "oturum bulunamadı"))
		return
	}

	user, err := s.store.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("kullanıcı okunamadı", err))
		return
	}
	user.passwordHash = ""
	httpx.JSON(w, http.StatusOK, user)
}

// handleChangePassword, mevcut şifreyi doğrulayıp yenisini kaydeder ve tüm
// oturumları kapatır.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := userFrom(r.Context())
	if claims == nil {
		httpx.Error(w, r, httpx.Unauthorized("unauthenticated", "oturum bulunamadı"))
		return
	}

	var req ChangePasswordRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(req.NewPassword) < 8 {
		httpx.Error(w, r, httpx.BadRequest("weak_password", "yeni şifre en az 8 karakter olmalı"))
		return
	}

	ctx := r.Context()
	user, err := s.store.GetUserByID(ctx, claims.UserID)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("kullanıcı okunamadı", err))
		return
	}

	if err := auth.VerifyPassword(req.CurrentPassword, user.passwordHash); err != nil {
		httpx.Error(w, r, httpx.Unauthorized("invalid_credentials", "mevcut şifre hatalı"))
		return
	}

	hash, err := auth.HashPassword(req.NewPassword, s.argonParams)
	if err != nil {
		httpx.Error(w, r, httpx.BadRequest("weak_password", err.Error()))
		return
	}

	if err := s.store.UpdatePassword(ctx, user.ID, hash); err != nil {
		httpx.Error(w, r, httpx.Internal("şifre güncellenemedi", err))
		return
	}

	// Şifre değişince tüm oturumlar kapatılır: çalınmış bir refresh token
	// şifre değişikliğinden sonra kullanılamamalıdır.
	if err := s.store.RevokeAllRefreshTokens(ctx, user.ID); err != nil {
		s.log.WarnContext(ctx, "oturumlar iptal edilemedi", "err", err.Error())
	}

	s.audit(ctx, r, "auth.password_changed", "admin_user", user.ID, nil, nil)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
