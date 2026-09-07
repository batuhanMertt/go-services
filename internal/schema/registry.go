// Package schema, event payload'larını JSON Schema ile doğrular.
//
// Neden JSON Schema (Avro/Protobuf değil)?
//   - Avro ve Protobuf ikili biçimlerdir; şema kaydı (schema registry) ve kod
//     üretimi zorunlu hale gelir. Bu, "yeni bir tool bağlanırken kod üretmesi
//     gerekmesin" hedefiyle çelişir.
//   - Event'ler zaten HTTP/JSON ile geliyor; ikili biçime çevirmek ek dönüşüm
//     maliyeti demek.
//   - JSON Schema, şemanın kendisini JSON olarak saklamayı sağlar → admin
//     panelinden bir event tipi tanımlamak kod dağıtımı gerektirmez.
//
// Bedeli: Avro'nun sağladığı otomatik şema evrimi kontrolü yok. Bunu
// versiyonlama disipliniyle telafi ediyoruz (docs/api-contracts/README.md):
// kırıcı değişiklik = yeni versiyon.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ErrSchemaNotFound, istenen event tipi için şema bulunamadı.
var ErrSchemaNotFound = errors.New("event tipi için şema tanımlı değil")

// ValidationError, alan bazlı doğrulama hatalarını taşır.
type ValidationError struct {
	Problems map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Problems))
	for k := range e.Problems {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("payload şemaya uymuyor")
	for _, k := range keys {
		b.WriteString("; ")
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(e.Problems[k])
	}
	return b.String()
}

// Registry, derlenmiş şemaları bellekte tutar.
//
// Derleme maliyetlidir; her istekte yeniden derlemek ingest gecikmesini
// (NFR-2.1: p95 < 50 ms) tek başına aşardı. Şemalar admin API'den
// değiştiğinde Reload ile tazelenir.
type Registry struct {
	mu       sync.RWMutex
	compiled map[string]*jsonschema.Schema
}

func NewRegistry() *Registry {
	return &Registry{compiled: make(map[string]*jsonschema.Schema)}
}

// Compile, ham JSON Schema'yı derleyip kaydeder. key = "user.created@1".
func (r *Registry) Compile(key string, raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("%s: şema boş", key)
	}

	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020

	resourceURL := "https://mindmons.local/schemas/" + key + ".json"
	if err := c.AddResource(resourceURL, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("%s: şema kaynağı eklenemedi: %w", key, err)
	}

	sch, err := c.Compile(resourceURL)
	if err != nil {
		return fmt.Errorf("%s: şema derlenemedi: %w", key, err)
	}

	r.mu.Lock()
	r.compiled[key] = sch
	r.mu.Unlock()
	return nil
}

// Replace, tüm şema kümesini atomik olarak değiştirir (periyodik yenileme).
func (r *Registry) Replace(schemas map[string][]byte) error {
	next := make(map[string]*jsonschema.Schema, len(schemas))

	for key, raw := range schemas {
		c := jsonschema.NewCompiler()
		c.Draft = jsonschema.Draft2020

		resourceURL := "https://mindmons.local/schemas/" + key + ".json"
		if err := c.AddResource(resourceURL, bytes.NewReader(raw)); err != nil {
			return fmt.Errorf("%s: şema kaynağı eklenemedi: %w", key, err)
		}
		sch, err := c.Compile(resourceURL)
		if err != nil {
			return fmt.Errorf("%s: şema derlenemedi: %w", key, err)
		}
		next[key] = sch
	}

	r.mu.Lock()
	r.compiled = next
	r.mu.Unlock()
	return nil
}

// Has, şemanın kayıtlı olup olmadığını söyler.
func (r *Registry) Has(key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.compiled[key]
	return ok
}

// Len, kayıtlı şema sayısı.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.compiled)
}

// Keys, kayıtlı şema anahtarlarını döner.
func (r *Registry) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.compiled))
	for k := range r.compiled {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate, payload'ı ilgili şemaya karşı doğrular.
func (r *Registry) Validate(key string, payload []byte) error {
	r.mu.RLock()
	sch, ok := r.compiled[key]
	r.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrSchemaNotFound, key)
	}

	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return &ValidationError{Problems: map[string]string{"payload": "geçerli JSON değil"}}
	}

	if err := sch.Validate(doc); err != nil {
		return &ValidationError{Problems: flatten(err)}
	}
	return nil
}

// flatten, jsonschema hata ağacını "alan → mesaj" haritasına indirger.
func flatten(err error) map[string]string {
	problems := map[string]string{}

	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		problems["payload"] = err.Error()
		return problems
	}

	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			field := strings.TrimPrefix(e.InstanceLocation, "/")
			field = strings.ReplaceAll(field, "/", ".")
			if field == "" {
				field = "payload"
			}
			if _, exists := problems[field]; !exists {
				problems[field] = e.Message
			}
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(ve)

	if len(problems) == 0 {
		problems["payload"] = ve.Error()
	}
	return problems
}
