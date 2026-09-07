package schema

import (
	"errors"
	"testing"
)

const userCreatedSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["user_id", "email", "status"],
  "additionalProperties": true,
  "properties": {
    "user_id": { "type": "string", "minLength": 1 },
    "email":   { "type": "string" },
    "status":  { "type": "string", "enum": ["active", "pending", "suspended"] },
    "score":   { "type": "number", "minimum": 0, "maximum": 100 }
  }
}`

func newRegistry(t *testing.T) *Registry {
	t.Helper()

	r := NewRegistry()
	if err := r.Compile("user.created@1", []byte(userCreatedSchema)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCompileAndValidate(t *testing.T) {
	r := newRegistry(t)

	if !r.Has("user.created@1") {
		t.Fatal("derlenen şema kayıtlı değil")
	}
	if r.Len() != 1 {
		t.Fatalf("Len() = %d, beklenen 1", r.Len())
	}

	valid := []byte(`{"user_id":"u1","email":"a@b.c","status":"active"}`)
	if err := r.Validate("user.created@1", valid); err != nil {
		t.Fatalf("geçerli payload reddedildi: %v", err)
	}
}

func TestValidateReportsFieldProblems(t *testing.T) {
	r := newRegistry(t)

	tests := []struct {
		name      string
		payload   string
		wantField string
	}{
		{"zorunlu alan eksik", `{"email":"a@b.c","status":"active"}`, "payload"},
		{"enum dışı değer", `{"user_id":"u1","email":"a@b.c","status":"deleted"}`, "status"},
		{"yanlış tip", `{"user_id":123,"email":"a@b.c","status":"active"}`, "user_id"},
		{"aralık dışı", `{"user_id":"u1","email":"a@b.c","status":"active","score":150}`, "score"},
		{"boş string", `{"user_id":"","email":"a@b.c","status":"active"}`, "user_id"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := r.Validate("user.created@1", []byte(tc.payload))
			if err == nil {
				t.Fatal("doğrulama hatası bekleniyordu")
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidationError bekleniyordu: %v", err)
			}
			if len(ve.Problems) == 0 {
				t.Fatal("alan bazlı hata detayı yok — istemci neyi düzelteceğini bilemez")
			}
		})
	}
}

func TestValidateAllowsExtraFields(t *testing.T) {
	r := newRegistry(t)

	// additionalProperties: true → şema evriminde ileri uyumluluk.
	payload := []byte(`{"user_id":"u1","email":"a@b.c","status":"active","yeni_alan":"v2'den geldi"}`)

	if err := r.Validate("user.created@1", payload); err != nil {
		t.Fatalf("ek alan reddedildi — ileri uyumluluk kırılıyor: %v", err)
	}
}

func TestValidateUnknownType(t *testing.T) {
	r := newRegistry(t)

	err := r.Validate("invoice.paid@1", []byte(`{}`))
	if !errors.Is(err, ErrSchemaNotFound) {
		t.Fatalf("beklenen ErrSchemaNotFound, alınan: %v", err)
	}
}

func TestValidateRejectsMalformedJSON(t *testing.T) {
	r := newRegistry(t)

	err := r.Validate("user.created@1", []byte(`{bozuk`))

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ValidationError bekleniyordu: %v", err)
	}
}

func TestCompileRejectsInvalidSchema(t *testing.T) {
	r := NewRegistry()

	invalid := map[string]string{
		"boş":                 ``,
		"bozuk JSON":          `{ this is not json`,
		"geçersiz tip adı":    `{"type": "nosuchtype"}`,
		"geçersiz kısıt tipi": `{"type":"object","required":"olmali-dizi"}`,
	}

	for name, raw := range invalid {
		if err := r.Compile("bad@1", []byte(raw)); err == nil {
			t.Errorf("%s: geçersiz şema kabul edildi", name)
		}
	}
}

// Replace atomiktir: yarıda hata olursa eski şema kümesi korunmalı.
func TestReplaceIsAtomic(t *testing.T) {
	r := newRegistry(t)

	err := r.Replace(map[string][]byte{
		"a@1": []byte(userCreatedSchema),
		"b@1": []byte(`{ bozuk`),
	})
	if err == nil {
		t.Fatal("bozuk şema içeren Replace başarılı döndü")
	}

	if !r.Has("user.created@1") {
		t.Fatal("başarısız Replace mevcut şemaları sildi")
	}
	if r.Has("a@1") {
		t.Fatal("başarısız Replace kısmen uygulandı")
	}
}

func TestReplaceSwapsSchemaSet(t *testing.T) {
	r := newRegistry(t)

	if err := r.Replace(map[string][]byte{
		"invoice.paid@1": []byte(`{"type":"object","required":["invoice_id"],"properties":{"invoice_id":{"type":"string"}}}`),
	}); err != nil {
		t.Fatal(err)
	}

	if r.Has("user.created@1") {
		t.Fatal("eski şema kümesi temizlenmedi")
	}
	if !r.Has("invoice.paid@1") {
		t.Fatal("yeni şema yüklenmedi")
	}
}

func TestKeysAreSorted(t *testing.T) {
	r := NewRegistry()
	_ = r.Replace(map[string][]byte{
		"c@1": []byte(`{"type":"object"}`),
		"a@1": []byte(`{"type":"object"}`),
		"b@1": []byte(`{"type":"object"}`),
	})

	keys := r.Keys()
	want := []string{"a@1", "b@1", "c@1"}

	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("Keys() sıralı değil: %v", keys)
		}
	}
}

func BenchmarkValidate(b *testing.B) {
	r := NewRegistry()
	_ = r.Compile("user.created@1", []byte(userCreatedSchema))
	payload := []byte(`{"user_id":"u1","email":"a@b.c","status":"active","score":85}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Validate("user.created@1", payload)
	}
}
