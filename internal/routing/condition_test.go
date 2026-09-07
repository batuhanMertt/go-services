package routing

import (
	"encoding/json"
	"errors"
	"testing"
)

// doc, testlerde kullanılan örnek değerlendirme belgesi.
func doc(t *testing.T) map[string]any {
	t.Helper()

	raw := `{
	  "event_type": "user.created",
	  "event_version": 1,
	  "source_tool": "billing-service",
	  "payload": {
	    "status": "active",
	    "score": 85,
	    "email": "ada@mindmons.local",
	    "tags": ["premium", "eu"],
	    "nested": { "deep": { "value": "found" } },
	    "items": [ { "sku": "A-1" }, { "sku": "B-2" } ],
	    "nullable": null
	  },
	  "metadata": { "tenant": "acme" }
	}`

	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLookup(t *testing.T) {
	d := doc(t)

	tests := []struct {
		path      string
		wantFound bool
		want      any
	}{
		{"event_type", true, "user.created"},
		{"payload.status", true, "active"},
		{"payload.score", true, float64(85)},
		{"payload.nested.deep.value", true, "found"},
		{"payload.items.0.sku", true, "A-1"},
		{"payload.items.1.sku", true, "B-2"},
		{"metadata.tenant", true, "acme"},
		{"payload.nullable", true, nil},
		{"payload.yok", false, nil},
		{"payload.items.9.sku", false, nil},
		{"payload.status.derin", false, nil}, // string'in içine inilemez
		{"", false, nil},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got, found := Lookup(d, tc.path)

			if found != tc.wantFound {
				t.Fatalf("found=%v, beklenen %v", found, tc.wantFound)
			}
			if found && got != tc.want {
				t.Fatalf("değer=%v (%T), beklenen %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestEmptyConditionMatchesEverything(t *testing.T) {
	// Kural yoksa abonelik koşulsuz eşleşir — bu, sistemin varsayılan
	// davranışıdır ve kırılırsa hiçbir event teslim edilmez.
	var c Condition
	if !c.Evaluate(doc(t)) {
		t.Fatal("boş koşul eşleşmedi")
	}

	parsed, err := Parse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Evaluate(doc(t)) {
		t.Fatal("{} koşulu eşleşmedi")
	}
}

func TestComparisonOperators(t *testing.T) {
	d := doc(t)

	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{"eq eşleşir", Condition{Op: OpEq, Path: "payload.status", Value: "active"}, true},
		{"eq eşleşmez", Condition{Op: OpEq, Path: "payload.status", Value: "pending"}, false},
		{"eq sayı", Condition{Op: OpEq, Path: "payload.score", Value: float64(85)}, true},
		{"eq int/float esnek", Condition{Op: OpEq, Path: "payload.score", Value: 85}, true},

		{"neq eşleşir", Condition{Op: OpNeq, Path: "payload.status", Value: "pending"}, true},
		{"neq olmayan alan true döner", Condition{Op: OpNeq, Path: "payload.yok", Value: "x"}, true},

		{"gt", Condition{Op: OpGt, Path: "payload.score", Value: 80}, true},
		{"gt sınırda false", Condition{Op: OpGt, Path: "payload.score", Value: 85}, false},
		{"gte sınırda true", Condition{Op: OpGte, Path: "payload.score", Value: 85}, true},
		{"lt", Condition{Op: OpLt, Path: "payload.score", Value: 90}, true},
		{"lte sınırda true", Condition{Op: OpLte, Path: "payload.score", Value: 85}, true},
		{"sayısal op metin alanda false", Condition{Op: OpGt, Path: "payload.status", Value: 5}, false},
		{"sayısal op olmayan alanda false", Condition{Op: OpGt, Path: "payload.yok", Value: 5}, false},

		{"in eşleşir", Condition{Op: OpIn, Path: "payload.status", Value: []any{"active", "pending"}}, true},
		{"in eşleşmez", Condition{Op: OpIn, Path: "payload.status", Value: []any{"deleted"}}, false},
		{"nin eşleşir", Condition{Op: OpNin, Path: "payload.status", Value: []any{"deleted"}}, true},

		{"contains dizide", Condition{Op: OpContains, Path: "payload.tags", Value: "premium"}, true},
		{"contains dizide yok", Condition{Op: OpContains, Path: "payload.tags", Value: "basic"}, false},
		{"contains metinde", Condition{Op: OpContains, Path: "payload.email", Value: "mindmons"}, true},

		{"exists true", Condition{Op: OpExists, Path: "payload.email", Value: true}, true},
		{"exists false — alan yok", Condition{Op: OpExists, Path: "payload.yok", Value: false}, true},
		{"exists true — alan yok", Condition{Op: OpExists, Path: "payload.yok", Value: true}, false},

		{"regex eşleşir", Condition{Op: OpRegex, Path: "payload.email", Value: `^ada@`}, true},
		{"regex eşleşmez", Condition{Op: OpRegex, Path: "payload.email", Value: `^bob@`}, false},

		{"derin yol", Condition{Op: OpEq, Path: "payload.nested.deep.value", Value: "found"}, true},
		{"dizi indeksi", Condition{Op: OpEq, Path: "payload.items.1.sku", Value: "B-2"}, true},
		{"zarf alanı", Condition{Op: OpEq, Path: "event_type", Value: "user.created"}, true},
		{"metadata alanı", Condition{Op: OpEq, Path: "metadata.tenant", Value: "acme"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cond.Evaluate(d); got != tc.want {
				t.Fatalf("sonuç=%v, beklenen %v", got, tc.want)
			}
		})
	}
}

func TestLogicalOperators(t *testing.T) {
	d := doc(t)

	match := Condition{Op: OpEq, Path: "payload.status", Value: "active"}
	noMatch := Condition{Op: OpEq, Path: "payload.status", Value: "deleted"}

	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{"and hepsi doğru", Condition{Op: OpAnd, Rules: []Condition{match, {Op: OpGte, Path: "payload.score", Value: 80}}}, true},
		{"and biri yanlış", Condition{Op: OpAnd, Rules: []Condition{match, noMatch}}, false},
		{"or biri doğru", Condition{Op: OpOr, Rules: []Condition{noMatch, match}}, true},
		{"or hepsi yanlış", Condition{Op: OpOr, Rules: []Condition{noMatch, noMatch}}, false},
		{"not doğruyu tersler", Condition{Op: OpNot, Rules: []Condition{match}}, false},
		{"not yanlışı tersler", Condition{Op: OpNot, Rules: []Condition{noMatch}}, true},
		{
			"iç içe: (A ve B) veya C",
			Condition{Op: OpOr, Rules: []Condition{
				{Op: OpAnd, Rules: []Condition{noMatch, match}},
				{Op: OpEq, Path: "metadata.tenant", Value: "acme"},
			}},
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cond.Evaluate(d); got != tc.want {
				t.Fatalf("sonuç=%v, beklenen %v", got, tc.want)
			}
		})
	}
}

// Bozuk kural admin-api'de reddedilmelidir; routing-engine sıcak yolda
// ayrıştırma hatasıyla uğraşmak zorunda kalmamalı.
func TestValidateRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"bilinmeyen operatör", `{"op":"between","path":"payload.score","value":5}`},
		{"op eksik", `{"path":"payload.status","value":"active"}`},
		{"karşılaştırmada path yok", `{"op":"eq","value":"active"}`},
		{"and alt kuralsız", `{"op":"and","rules":[]}`},
		{"not birden fazla kural", `{"op":"not","rules":[{"op":"eq","path":"a","value":1},{"op":"eq","path":"b","value":2}]}`},
		{"in dizi değil", `{"op":"in","path":"payload.status","value":"active"}`},
		{"regex metin değil", `{"op":"regex","path":"payload.email","value":123}`},
		{"regex derlenemiyor", `{"op":"regex","path":"payload.email","value":"[unclosed"}`},
		{"iç içe bozuk kural", `{"op":"and","rules":[{"op":"eq","path":"a","value":1},{"op":"nope"}]}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.raw)); !errors.Is(err, ErrInvalidCondition) {
				t.Fatalf("beklenen ErrInvalidCondition, alınan: %v", err)
			}
		})
	}
}

func TestValidateAcceptsValid(t *testing.T) {
	valid := []string{
		`{}`,
		`null`,
		`{"op":"eq","path":"payload.status","value":"active"}`,
		`{"op":"exists","path":"payload.email"}`,
		`{"op":"in","path":"payload.status","value":["a","b"]}`,
		`{"op":"and","rules":[{"op":"eq","path":"a","value":1}]}`,
		`{"op":"not","rules":[{"op":"eq","path":"a","value":1}]}`,
		`{"op":"regex","path":"payload.email","value":"^ada@"}`,
	}

	for _, raw := range valid {
		if _, err := Parse([]byte(raw)); err != nil {
			t.Errorf("geçerli koşul reddedildi %s: %v", raw, err)
		}
	}
}

func TestBuildDocument(t *testing.T) {
	d := BuildDocument("user.created", 2, "billing", "trace-1",
		[]byte(`{"status":"active"}`), map[string]any{"tenant": "acme"})

	checks := map[string]any{
		"event_type":    "user.created",
		"event_version": 2,
		"source_tool":   "billing",
		"trace_id":      "trace-1",
	}
	for k, want := range checks {
		if d[k] != want {
			t.Errorf("%s = %v, beklenen %v", k, d[k], want)
		}
	}

	if v, ok := Lookup(d, "payload.status"); !ok || v != "active" {
		t.Errorf("payload.status çözülemedi: %v", v)
	}
	if v, ok := Lookup(d, "metadata.tenant"); !ok || v != "acme" {
		t.Errorf("metadata.tenant çözülemedi: %v", v)
	}
}

func TestBuildDocumentToleratesBadPayload(t *testing.T) {
	// Bozuk payload panik yaratmamalı; yalnızca payload alanı eksik kalır.
	d := BuildDocument("x.y", 1, "tool", "t", []byte(`not json`), nil)

	if _, ok := d["payload"]; ok {
		t.Fatal("bozuk payload belgeye eklenmemeliydi")
	}
	if d["event_type"] != "x.y" {
		t.Fatal("zarf alanları korunmalıydı")
	}
}

func BenchmarkEvaluate(b *testing.B) {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{"event_type":"user.created","payload":{"status":"active","score":85}}`), &m)

	cond := Condition{Op: OpAnd, Rules: []Condition{
		{Op: OpEq, Path: "payload.status", Value: "active"},
		{Op: OpGte, Path: "payload.score", Value: 80},
	}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cond.Evaluate(m)
	}
}
