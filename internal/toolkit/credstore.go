package toolkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CredStore, tool'un aldığı API anahtarını kalıcı olarak saklar.
//
// Neden dosya? Bir tool'un API anahtarı üretimi TEK SEFERLİKTİR — plaintext
// bir daha gösterilmez. Container yeniden başladığında anahtar kaybolursa
// tool her açılışta yeni anahtar üretir ve DB'de çöp birikir. Gerçek bir
// tool bu değeri kendi secret yönetiminde (Vault, K8s Secret, .env) tutar;
// mock tool'lar için bind-mount edilmiş bir dosya aynı rolü oynar.
//
// Dosya 0600 izniyle yazılır: aynı host'taki diğer kullanıcılar okuyamaz.
type CredStore struct {
	path string
}

func NewCredStore(path string) *CredStore {
	return &CredStore{path: path}
}

// Load, saklanmış kimlik bilgilerini okur. Dosya yoksa (nil, nil) döner.
func (s *CredStore) Load() (*Credentials, error) {
	if s.path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kimlik dosyası okunamadı: %w", err)
	}

	// Bozuk veya eksik dosya HATA DEĞİLDİR: "saklanmış kimlik yok" anlamına
	// gelir ve çağıran taraf yeniden kayıt olur. Hata döndürmek, tool'un
	// bozuk bir dosya yüzünden hiç açılamamasına yol açardı.
	var c Credentials
	if unmarshalErr := json.Unmarshal(data, &c); unmarshalErr != nil {
		return nil, nil //nolint:nilerr // bilinçli: bozuk dosya = kimlik yok
	}
	if c.APIKey == "" || c.HMACSecret == "" {
		return nil, nil
	}
	return &c, nil
}

// Save, kimlik bilgilerini diske yazar.
func (s *CredStore) Save(c *Credentials) error {
	if s.path == "" {
		return nil
	}

	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("kimlik dizini oluşturulamadı: %w", err)
		}
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("kimlik dosyası yazılamadı: %w", err)
	}
	return nil
}
