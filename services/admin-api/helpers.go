package main

import (
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mindmons/integration-hub/internal/httpx"
)

// notFoundOr, ErrNotFound'u 404'e, diğerlerini 500'e çevirir.
func notFoundOr(err error, msg string) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("not_found", msg)
	}
	return httpx.Internal("kayıt okunamadı", err)
}

// Postgres SQLSTATE kodları.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
)

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// isUniqueViolation, benzersizlik ihlalini tanır → 409 Conflict.
func isUniqueViolation(err error) bool { return pgCode(err) == sqlStateUniqueViolation }

// isForeignKeyViolation, geçersiz referansı tanır → 400 Bad Request.
func isForeignKeyViolation(err error) bool { return pgCode(err) == sqlStateForeignKeyViolation }

// isCheckViolation, CHECK kısıtı ihlalini tanır (geçersiz enum değeri).
func isCheckViolation(err error) bool { return pgCode(err) == sqlStateCheckViolation }

// mapWriteError, yazma hatalarını doğru HTTP durum koduna çevirir.
//
// Neden gerekli? Veritabanı kısıtlarının ihlali İSTEMCİ hatasıdır, sunucu
// hatası değil. Hepsini 500 döndürmek istemciye "bende bir sorun var" der;
// oysa çoğu zaman gönderilen veri hatalıdır ve istemci bunu düzeltebilir.
func mapWriteError(err error, conflictCode, conflictMsg string) error {
	switch {
	case isUniqueViolation(err):
		return httpx.Conflict(conflictCode, conflictMsg)
	case isForeignKeyViolation(err):
		return httpx.BadRequest("invalid_reference", "referans verilen kayıt bulunamadı")
	case isCheckViolation(err):
		return httpx.BadRequest("invalid_value",
			"alan değerlerinden biri izin verilen kümede değil (örn. kind, status)")
	default:
		return httpx.Internal("kayıt yazılamadı", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
