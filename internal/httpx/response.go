// Package httpx, tüm HTTP servislerinin paylaştığı yanıt biçimi, hata modeli
// ve middleware zincirini içerir. Tek biçimli hata gövdesi, OpenAPI
// kontratının da temelidir (docs/api-contracts/).
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ErrorBody, platformun TÜM servislerinde kullanılan tek hata biçimi.
type ErrorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Details map[string]string `json:"details,omitempty"`
		TraceID string            `json:"trace_id,omitempty"`
	} `json:"error"`
}

// APIError, handler'ların döndürebileceği yapılandırılmış hata tipi.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details map[string]string
	Err     error
}

func (e *APIError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *APIError) Unwrap() error { return e.Err }

// Yaygın hata kurucuları — kod içinde tekrar eden if bloklarını azaltır.
func BadRequest(code, msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: code, Message: msg}
}

func Unauthorized(code, msg string) *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: code, Message: msg}
}

func Forbidden(code, msg string) *APIError {
	return &APIError{Status: http.StatusForbidden, Code: code, Message: msg}
}

func NotFound(code, msg string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: code, Message: msg}
}

func Conflict(code, msg string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: code, Message: msg}
}

func TooManyRequests(code, msg string) *APIError {
	return &APIError{Status: http.StatusTooManyRequests, Code: code, Message: msg}
}

func PayloadTooLarge(code, msg string) *APIError {
	return &APIError{Status: http.StatusRequestEntityTooLarge, Code: code, Message: msg}
}

func Internal(msg string, err error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: msg, Err: err}
}

// WithDetails, hataya alan bazlı ayrıntı ekler (validation sonuçları gibi).
func (e *APIError) WithDetails(d map[string]string) *APIError {
	e.Details = d
	return e
}

// JSON, verilen gövdeyi belirtilen durum koduyla yazar.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("yanıt serileştirilemedi", slog.String("err", err.Error()))
	}
}

// NoContent, gövdesiz 204 yanıtı yazar.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// Error, bir hatayı tek biçimli JSON gövdesine çevirip yazar.
// İç hataların detayı istemciye SIZDIRILMAZ; yalnızca loglanır (bilgi ifşası).
func Error(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = Internal("beklenmeyen bir hata oluştu", err)
	}

	traceID := TraceIDFromRequest(r)

	if apiErr.Status >= 500 {
		slog.ErrorContext(r.Context(), "istek başarısız",
			slog.String("code", apiErr.Code),
			slog.String("err", apiErr.Error()),
			slog.String("path", r.URL.Path),
		)
	}

	var body ErrorBody
	body.Error.Code = apiErr.Code
	body.Error.Message = apiErr.Message
	body.Error.Details = apiErr.Details
	body.Error.TraceID = traceID

	JSON(w, apiErr.Status, body)
}

// DecodeJSON, gövdeyi hedefe çözer ve bilinmeyen alanlarda hata verir.
// Katı çözümleme (DisallowUnknownFields), kontrat dışı alanların sessizce
// yutulmasını engeller — contract-first yaklaşımının doğal sonucu.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return BadRequest("invalid_json", "istek gövdesi geçerli JSON değil: "+err.Error())
	}
	return nil
}
