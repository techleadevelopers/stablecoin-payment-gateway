package privacy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

type Codec struct {
	key []byte
}

func New(secret string) (*Codec, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("LGPD_SECRET nao configurado")
	}
	sum := sha256.Sum256([]byte(secret))
	return &Codec{key: sum[:]}, nil
}

func Hash(value, secret string) string {
	value = normalize(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret) + ":" + value))
	return hex.EncodeToString(sum[:])
}

func (c *Codec) Encrypt(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	raw := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

func (c *Codec) Decrypt(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext invalido")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func normalize(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.NewReplacer(".", "", "-", "", " ", "", "(", "", ")", "", "+", "").Replace(value)
	return value
}
