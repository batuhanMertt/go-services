// Package routing, admin tarafından tanımlanan yönlendirme kurallarını
// değerlendirir.
//
// Kural dili neden JSONPath/JQ değil?
//   - JQ tam bir programlama dilidir; kullanıcı tanımlı bir ifade sonsuz
//     döngüye girebilir veya beklenmedik biçimde pahalı olabilir. Sıcak yolda
//     (her event için) çalışan bir bileşende bu kabul edilemez.
//   - JSONPath kütüphaneleri arasında sözdizimi tutarsızdır ve hata mesajları
//     admin kullanıcısı için anlaşılmazdır.
//   - Bu projenin gerçek ihtiyacı ("payload'da şu alan şu değere eşitse
//     gönder") küçük ve kapalı bir operatör kümesiyle karşılanır.
//
// Seçilen çözüm: JSON olarak saklanan, noktalı yol + sabit operatör kümesi
// kullanan bir koşul ağacı. Sonlanması garanti, tamamı unit-test edilebilir,
// admin UI'da form olarak render edilebilir ve hata mesajları anlamlıdır.
//
// Örnek:
//
//	{"op":"and","rules":[
//	  {"path":"payload.status","op":"eq","value":"active"},
//	  {"path":"payload.score","op":"gte","value":80}
//	]}
package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Operatörler.
const (
	OpAnd      = "and"
	OpOr       = "or"
	OpNot      = "not"
	OpEq       = "eq"
	OpNeq      = "neq"
	OpGt       = "gt"
	OpGte      = "gte"
	OpLt       = "lt"
	OpLte      = "lte"
	OpIn       = "in"
	OpNin      = "nin"
	OpContains = "contains"
	OpExists   = "exists"
	OpRegex    = "regex"
)

var ErrInvalidCondition = errors.New("yönlendirme koşulu geçersiz")

// Condition, koşul ağacının bir düğümü.
type Condition struct {
	Op    string      `json:"op,omitempty"`
	Path  string      `json:"path,omitempty"`
	Value any         `json:"value,omitempty"`
	Rules []Condition `json:"rules,omitempty"`
}

// IsEmpty, "koşulsuz eşleş" anlamına gelen boş koşulu tanır.
func (c Condition) IsEmpty() bool {
	return c.Op == "" && c.Path == "" && len(c.Rules) == 0
}

// Parse, JSONB alanından okunan ham koşulu çözer ve doğrular.
func Parse(raw []byte) (Condition, error) {
	var c Condition
	if len(raw) == 0 || string(raw) == "null" {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%w: %w", ErrInvalidCondition, err)
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// Validate, koşulun yapısal geçerliliğini kontrol eder.
// Kural KAYDEDİLİRKEN çağrılır; bozuk kural asla veritabanına girmez, böylece
// routing engine sıcak yolda hata ayıklamak zorunda kalmaz.
func (c Condition) Validate() error {
	if c.IsEmpty() {
		return nil
	}

	switch c.Op {
	case OpAnd, OpOr:
		if len(c.Rules) == 0 {
			return fmt.Errorf("%w: %q en az bir alt kural gerektirir", ErrInvalidCondition, c.Op)
		}
		for _, r := range c.Rules {
			if err := r.Validate(); err != nil {
				return err
			}
		}
		return nil

	case OpNot:
		if len(c.Rules) != 1 {
			return fmt.Errorf("%w: %q tam olarak bir alt kural gerektirir", ErrInvalidCondition, c.Op)
		}
		return c.Rules[0].Validate()

	case OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpContains, OpExists, OpRegex:
		if strings.TrimSpace(c.Path) == "" {
			return fmt.Errorf("%w: %q için path zorunlu", ErrInvalidCondition, c.Op)
		}
		if c.Op == OpIn || c.Op == OpNin {
			if _, ok := c.Value.([]any); !ok {
				return fmt.Errorf("%w: %q için value dizi olmalı", ErrInvalidCondition, c.Op)
			}
		}
		if c.Op == OpRegex {
			s, ok := c.Value.(string)
			if !ok {
				return fmt.Errorf("%w: regex için value metin olmalı", ErrInvalidCondition)
			}
			if _, err := regexp.Compile(s); err != nil {
				return fmt.Errorf("%w: regex derlenemedi: %w", ErrInvalidCondition, err)
			}
		}
		return nil

	case "":
		return fmt.Errorf("%w: op alanı zorunlu", ErrInvalidCondition)

	default:
		return fmt.Errorf("%w: bilinmeyen operatör %q", ErrInvalidCondition, c.Op)
	}
}

// Evaluate, koşulu belge üzerinde değerlendirir.
// doc kökü şu alanları içerir: event_type, event_version, source_tool,
// trace_id, payload, metadata.
func (c Condition) Evaluate(doc map[string]any) bool {
	if c.IsEmpty() {
		return true // koşulsuz kural: her event eşleşir
	}

	switch c.Op {
	case OpAnd:
		for _, r := range c.Rules {
			if !r.Evaluate(doc) {
				return false
			}
		}
		return true

	case OpOr:
		for _, r := range c.Rules {
			if r.Evaluate(doc) {
				return true
			}
		}
		return false

	case OpNot:
		if len(c.Rules) != 1 {
			return false
		}
		return !c.Rules[0].Evaluate(doc)
	}

	actual, found := Lookup(doc, c.Path)

	switch c.Op {
	case OpExists:
		want, ok := c.Value.(bool)
		if !ok {
			want = true
		}
		return found == want

	case OpEq:
		return found && looseEqual(actual, c.Value)

	case OpNeq:
		// Alan yoksa "eşit değil" doğrudur — beklenen sezgisel davranış.
		return !found || !looseEqual(actual, c.Value)

	case OpGt, OpGte, OpLt, OpLte:
		if !found {
			return false
		}
		a, okA := toNumber(actual)
		b, okB := toNumber(c.Value)
		if !okA || !okB {
			return false
		}
		switch c.Op {
		case OpGt:
			return a > b
		case OpGte:
			return a >= b
		case OpLt:
			return a < b
		default:
			return a <= b
		}

	case OpIn, OpNin:
		list, ok := c.Value.([]any)
		if !ok {
			return false
		}
		match := false
		if found {
			for _, item := range list {
				if looseEqual(actual, item) {
					match = true
					break
				}
			}
		}
		if c.Op == OpIn {
			return match
		}
		return !match

	case OpContains:
		if !found {
			return false
		}
		return containsValue(actual, c.Value)

	case OpRegex:
		if !found {
			return false
		}
		pattern, ok := c.Value.(string)
		if !ok {
			return false
		}
		re, err := compiledRegex(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(toString(actual))

	default:
		return false
	}
}

// Lookup, noktalı yolu belge içinde çözer. Dizi indeksi desteklenir:
// "payload.items.0.sku".
func Lookup(doc map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}

	var current any = doc
	for _, segment := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			v, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = v

		case []any:
			idx, err := strconv.Atoi(segment)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			current = node[idx]

		default:
			return nil, false
		}
	}
	return current, true
}

// looseEqual, JSON'dan gelen değerleri makul biçimde karşılaştırır.
// JSON'da tüm sayılar float64'tür; 80 ile 80.0 aynı sayılmalıdır.
func looseEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	if na, ok := toNumber(a); ok {
		if nb, ok2 := toNumber(b); ok2 {
			return na == nb
		}
	}

	if ba, ok := a.(bool); ok {
		if bb, ok2 := b.(bool); ok2 {
			return ba == bb
		}
	}

	sa, okA := a.(string)
	sb, okB := b.(string)
	if okA && okB {
		return sa == sb
	}

	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// containsValue, metinde alt dize veya dizide eleman arar.
func containsValue(actual, want any) bool {
	switch node := actual.(type) {
	case string:
		return strings.Contains(node, toString(want))
	case []any:
		for _, item := range node {
			if looseEqual(item, want) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// regexCache, aynı kalıbın her event için yeniden derlenmesini önler.
var regexCache sync.Map // pattern -> *regexp.Regexp

func compiledRegex(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(pattern); ok {
		if re, isRegex := v.(*regexp.Regexp); isRegex {
			return re, nil
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache.Store(pattern, re)
	return re, nil
}

// BuildDocument, event zarfından değerlendirme belgesi üretir.
func BuildDocument(eventType string, eventVersion int, sourceTool, traceID string, payload []byte, metadata map[string]any) map[string]any {
	doc := map[string]any{
		"event_type":    eventType,
		"event_version": eventVersion,
		"source_tool":   sourceTool,
		"trace_id":      traceID,
	}

	var p any
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err == nil {
			doc["payload"] = p
		}
	}
	if metadata != nil {
		doc["metadata"] = metadata
	}
	return doc
}
