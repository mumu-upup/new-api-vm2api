package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
)

type RedemptionPartner struct {
	ID               uint      `json:"id"`
	PartnerID        string    `json:"partner_id" gorm:"uniqueIndex;type:varchar(128)"`
	SecretCiphertext string    `json:"-" gorm:"type:text"`
	Secret           string    `json:"-" gorm:"-"`
	Enabled          bool      `json:"enabled"`
	IPAllowlist      string    `json:"ip_allowlist" gorm:"type:text"`
	RateLimit        int       `json:"rate_limit" gorm:"default:60"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// BeforeSave encrypts a transient plaintext secret supplied by provisioning
// code. The Secret field is never mapped to a database column; only the
// ciphertext is persisted.
func (partner *RedemptionPartner) BeforeSave(_ *gorm.DB) error {
	if partner.Secret == "" {
		return nil
	}
	ciphertext, err := EncryptPartnerSecret(partner.Secret)
	if err != nil {
		return err
	}
	partner.SecretCiphertext = ciphertext
	partner.Secret = ""
	return nil
}

func encryptionKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv("REDEMPTION_ENCRYPTION_KEY"))
	if raw == "" {
		return nil, errors.New("REDEMPTION_ENCRYPTION_KEY is not configured")
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, errors.New("REDEMPTION_ENCRYPTION_KEY must be a base64-encoded or raw 32-byte key")
}

func EncryptPartnerSecret(secret string) (string, error) {
	if secret == "" {
		return "", errors.New("partner secret cannot be empty")
	}
	key, err := encryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(secret), nil)
	return base64.RawURLEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func decryptPartnerSecret(ciphertext string) (string, error) {
	key, err := encryptionKey()
	if err != nil {
		return "", err
	}
	data, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", errors.New("invalid partner secret ciphertext")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("invalid partner secret ciphertext")
	}
	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("invalid partner secret ciphertext")
	}
	return string(plaintext), nil
}

func (partner *RedemptionPartner) PrepareSecret() error {
	if partner.Secret == "" {
		return errors.New("partner secret cannot be empty")
	}
	ciphertext, err := EncryptPartnerSecret(partner.Secret)
	if err != nil {
		return err
	}
	partner.SecretCiphertext = ciphertext
	partner.Secret = ""
	return nil
}

func GetRedemptionPartner(partnerID string) (*RedemptionPartner, error) {
	if strings.TrimSpace(partnerID) == "" {
		return nil, errors.New("partner id is required")
	}
	partner := &RedemptionPartner{}
	if err := DB.Where("partner_id = ?", partnerID).First(partner).Error; err != nil {
		return nil, err
	}
	if !partner.Enabled {
		return nil, errors.New("partner is disabled")
	}
	secret, err := decryptPartnerSecret(partner.SecretCiphertext)
	if err != nil {
		return nil, fmt.Errorf("load partner secret: %w", err)
	}
	partner.Secret = secret
	return partner, nil
}
