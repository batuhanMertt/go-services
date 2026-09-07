package main

import (
	"net/http"
	"os"
)

// specPath, OpenAPI kontratının image içindeki yolu.
//
// Spec dosyası kod içine gömülmez (go:embed), image'a KOPYALANIR. Gerekçe:
// tek doğruluk kaynağı docs/api-contracts/ altındadır ve contract-first
// yaklaşımında bu dosya kodun değil, kodun uyduğu sözleşmenin parçasıdır.
// Kopyalamak yerine gömmek, dosyayı iki yerde tutmayı gerektirirdi.
func specPath() string {
	if p := os.Getenv("OPENAPI_SPEC_PATH"); p != "" {
		return p
	}
	return "/app/openapi.yaml"
}

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(specPath())
	if err != nil {
		s.log.WarnContext(r.Context(), "openapi spec okunamadı",
			"path", specPath(), "err", err.Error())
		http.Error(w, "openapi spec bulunamadı", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// swaggerHTML, Swagger UI'ı CDN'den yükleyen minimal sayfa.
// Sürüm sabitlenmiştir (reproducibility kuralı: latest kullanılmaz).
const swaggerHTML = `<!DOCTYPE html>
<html lang="tr">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Integration Hub · Admin API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: './openapi.yaml',
        dom_id: '#swagger-ui',
        deepLinking: true,
        persistAuthorization: true,
        presets: [SwaggerUIBundle.presets.apis],
        layout: 'BaseLayout'
      });
    };
  </script>
</body>
</html>`

func (s *Server) handleSwaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}
