package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type GPTAccountSecrets struct {
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
}

var ErrGPTAccountEncryption = errors.New("encryption_unavailable")

func gptAccountCipher() (cipher.AEAD, error) {
	path := strings.TrimSpace(os.Getenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE"))
	if path == "" {
		return nil, ErrGPTAccountEncryption
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrGPTAccountEncryption
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrGPTAccountEncryption
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, ErrGPTAccountEncryption
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrGPTAccountEncryption
	}
	return cipher.NewGCM(block)
}

func GPTAccountEncryptionAvailable() bool { _, err := gptAccountCipher(); return err == nil }

func encryptGPTSecret(value, binding string) (string, error) {
	aead, err := gptAccountCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", ErrGPTAccountEncryption
	}
	return "v1." + base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(value), []byte(binding))), nil
}

func decryptGPTSecret(value, binding string) (string, error) {
	aead, err := gptAccountCipher()
	if err != nil {
		return "", err
	}
	encoded, ok := strings.CutPrefix(value, "v1.")
	if !ok {
		return "", ErrGPTAccountEncryption
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) < aead.NonceSize()+aead.Overhead() {
		return "", ErrGPTAccountEncryption
	}
	decoded, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(binding))
	if err != nil {
		return "", ErrGPTAccountEncryption
	}
	return string(decoded), nil
}

func EncryptGPTAccountSecrets(account *model.GPTAccount, secrets GPTAccountSecrets) error {
	encoded, err := common.Marshal(secrets)
	if err != nil {
		return ErrGPTAccountEncryption
	}
	account.SecretsCiphertext, err = encryptGPTSecret(string(encoded), "gpt-account:"+account.SecretID)
	account.HasPassword, account.HasTOTP = secrets.Password != "", secrets.TOTPSecret != ""
	return err
}

func DecryptGPTAccountSecrets(account *model.GPTAccount) (*GPTAccountSecrets, error) {
	value, err := decryptGPTSecret(account.SecretsCiphertext, "gpt-account:"+account.SecretID)
	if err != nil {
		return nil, err
	}
	var secrets GPTAccountSecrets
	if common.UnmarshalJsonStr(value, &secrets) != nil {
		return nil, ErrGPTAccountEncryption
	}
	return &secrets, nil
}
