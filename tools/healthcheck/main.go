// Command healthcheck, distroless image'lar için minimal sağlık kontrolüdür.
//
// Neden gerekli? Servisler `gcr.io/distroless/static` üzerinde çalışır —
// içinde shell, curl veya wget YOKTUR (saldırı yüzeyini küçültmenin bedeli).
// Docker HEALTHCHECK bir komut çalıştırmak zorunda olduğu için, aynı image'a
// gömülen bu ~2 MB'lık ikili dosya o rolü üstlenir.
//
// Kullanım: HEALTHCHECK_URL env değişkeni ile hedef belirlenir.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	url := os.Getenv("HEALTHCHECK_URL")
	if url == "" {
		port := os.Getenv("HEALTHCHECK_PORT")
		if port == "" {
			port = "8080"
		}
		url = "http://127.0.0.1:" + port + "/healthz"
	}

	// Docker healthcheck'in kendi timeout'u var, ancak istemci tarafında da
	// sınır koymak asılı kalan bir bağlantının container'ı "unhealthy" yerine
	// belirsiz durumda bırakmasını engeller.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: istek oluşturulamadı: %v\n", err)
		os.Exit(1)
	}

	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "healthcheck: %s → %d\n", url, resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
