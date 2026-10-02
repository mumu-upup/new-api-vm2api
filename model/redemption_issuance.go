package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	RedemptionIssuanceStatusIssued   = "issued"
	RedemptionIssuanceStatusRedeemed = "redeemed"
	RedemptionIssuanceStatusExpired  = "expired"
	RedemptionIssuanceStatusRevoked  = "revoked"
)

var (
	ErrProductNotFound        = errors.New("product not found")
	ErrOrderParameterMismatch = errors.New("order parameters do not match")
	ErrOrderNotFound          = errors.New("order not found")
	ErrOrderAlreadyRedeemed   = errors.New("order already redeemed")
	ErrOrderExpired           = errors.New("order expired")
	ErrOrderRevoked           = errors.New("order revoked")
)

var issuanceMu sync.Mutex

type RedemptionIssuanceOrder struct {
	ID              uint      `json:"order_id"`
	PartnerID       string    `json:"partner_id" gorm:"index:idx_issuance_partner_merchant,unique;type:varchar(128)"`
	MerchantOrderID string    `json:"merchant_order_id" gorm:"index:idx_issuance_partner_merchant,unique;type:varchar(128)"`
	ProductCode     string    `json:"product_code" gorm:"type:varchar(32)"`
	Amount          int64     `json:"amount"`
	Quota           int       `json:"quota"`
	RedemptionID    uint      `json:"redemption_id" gorm:"index"`
	Status          string    `json:"status" gorm:"type:varchar(16);index"`
	IssuedAt        int64     `json:"issued_at"`
	ExpiresAt       int64     `json:"expires_at"`
	RedeemedAt      int64     `json:"redeemed_at"`
	RevokedAt       int64     `json:"revoked_at"`
	RevokedReason   string    `json:"-" gorm:"type:varchar(255)"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (RedemptionIssuanceOrder) TableName() string { return "redemption_issuance_orders" }

func randomRedemptionCode() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func partnerRedemptionMarker() string {
	// Redemption.Key is retained for backwards compatibility and has a unique
	// index. Partner codes are stored only in KeyHash/KeyCiphertext, so use a
	// unique opaque marker here rather than the plaintext code (or an empty
	// value that would collide with every other partner redemption).
	return "partner-" + common.GetUUID()
}

func hashRedemptionCode(code string) string {
	hash := sha256.Sum256([]byte(code))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func hashRedemptionCodePtr(code string) *string {
	hash := hashRedemptionCode(code)
	return &hash
}

func encryptRedemptionCode(code string) (string, error) {
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
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, []byte(code), nil)...)), nil
}

func decryptRedemptionCode(ciphertext string) (string, error) {
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
	data, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil || len(data) < gcm.NonceSize() {
		return "", errors.New("invalid redemption ciphertext")
	}
	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("invalid redemption ciphertext")
	}
	return string(plaintext), nil
}

func IssuePartnerRedemption(partnerID, merchantOrderID, productCode string) (*RedemptionIssuanceOrder, string, error) {
	issuanceMu.Lock()
	defer issuanceMu.Unlock()
	partnerID = strings.TrimSpace(partnerID)
	merchantOrderID = strings.TrimSpace(merchantOrderID)
	productCode = strings.TrimSpace(productCode)
	if partnerID == "" || merchantOrderID == "" {
		return nil, "", ErrOrderNotFound
	}
	product, err := GetEnabledRedemptionProduct(productCode)
	if err != nil {
		return nil, "", err
	}
	var order RedemptionIssuanceOrder
	var code string
	var terminalErr error
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := lockForUpdate(tx).Where("partner_id = ? AND merchant_order_id = ?", partnerID, merchantOrderID).First(&order)
		if result.Error == nil {
			if order.ProductCode != product.ProductCode {
				return ErrOrderParameterMismatch
			}
			if err := loadExistingPartnerOrder(tx, &order, &code); err != nil {
				// Expiration is a terminal state transition that must commit
				// before the caller receives ErrOrderExpired.
				if errors.Is(err, ErrOrderExpired) {
					terminalErr = err
					return nil
				}
				return err
			}
			return nil
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		code, err = randomRedemptionCode()
		if err != nil {
			return err
		}
		ciphertext, err := encryptRedemptionCode(code)
		if err != nil {
			return err
		}
		now := common.GetTimestamp()
		redemption := &Redemption{
			Name:          "partner:" + product.ProductCode,
			Key:           partnerRedemptionMarker(),
			KeyHash:       hashRedemptionCodePtr(code),
			KeyCiphertext: ciphertext,
			Source:        "partner_api",
			Status:        common.RedemptionCodeStatusEnabled,
			Quota:         product.Quota,
			CreatedTime:   now,
			ExpiredTime:   now + product.ValiditySeconds,
		}
		if err := tx.Create(redemption).Error; err != nil {
			return err
		}
		order = RedemptionIssuanceOrder{PartnerID: partnerID, MerchantOrderID: merchantOrderID, ProductCode: product.ProductCode, Amount: product.Amount, Quota: product.Quota, RedemptionID: uint(redemption.Id), Status: RedemptionIssuanceStatusIssued, IssuedAt: now, ExpiresAt: redemption.ExpiredTime}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		return tx.Model(redemption).Update("issuance_order_id", order.ID).Error
	})
	if err != nil {
		// The database unique constraint is the authority for idempotency across
		// multiple API instances. If another instance won the race, recover its
		// order and return the same code instead of surfacing a duplicate-key
		// error. Keep the original error when no order exists (for example, a
		// database outage or an unrelated constraint failure).
		if recoveredOrder, recoveredCode, recovered, recoverErr := recoverPartnerOrder(partnerID, merchantOrderID, product.ProductCode); recovered {
			if recoverErr != nil {
				return nil, "", recoverErr
			}
			return recoveredOrder, recoveredCode, nil
		}
		return nil, "", err
	}
	if terminalErr != nil {
		return nil, "", terminalErr
	}
	return &order, code, nil
}

func loadExistingPartnerOrder(tx *gorm.DB, order *RedemptionIssuanceOrder, code *string) error {
	switch order.Status {
	case RedemptionIssuanceStatusIssued:
		if order.ExpiresAt != 0 && order.ExpiresAt <= common.GetTimestamp() {
			if err := markPartnerOrderExpired(tx, order); err != nil {
				return err
			}
			return ErrOrderExpired
		}
		redemption := &Redemption{}
		if err := lockForUpdate(tx).First(redemption, order.RedemptionID).Error; err != nil {
			return err
		}
		if redemption.Status != common.RedemptionCodeStatusEnabled {
			return ErrOrderAlreadyRedeemed
		}
		if redemption.ExpiredTime != 0 && redemption.ExpiredTime <= common.GetTimestamp() {
			if err := markPartnerOrderExpired(tx, order); err != nil {
				return err
			}
			return ErrOrderExpired
		}
		var err error
		*code, err = decryptRedemptionCode(redemption.KeyCiphertext)
		return err
	case RedemptionIssuanceStatusRedeemed:
		return ErrOrderAlreadyRedeemed
	case RedemptionIssuanceStatusExpired:
		return ErrOrderExpired
	case RedemptionIssuanceStatusRevoked:
		return ErrOrderRevoked
	default:
		return ErrOrderNotFound
	}
}

func markPartnerOrderExpired(tx *gorm.DB, order *RedemptionIssuanceOrder) error {
	result := tx.Model(&RedemptionIssuanceOrder{}).
		Where("id = ? AND status = ?", order.ID, RedemptionIssuanceStatusIssued).
		Updates(map[string]any{"status": RedemptionIssuanceStatusExpired})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		order.Status = RedemptionIssuanceStatusExpired
	}
	// Disabling is intentionally best-effort when the code was already
	// consumed/revoked by a legacy deployment; the order lock still prevents
	// returning a code after this point.
	return tx.Model(&Redemption{}).
		Where("id = ? AND status = ?", order.RedemptionID, common.RedemptionCodeStatusEnabled).
		Update("status", common.RedemptionCodeStatusDisabled).Error
}

func recoverPartnerOrder(partnerID, merchantOrderID, productCode string) (*RedemptionIssuanceOrder, string, bool, error) {
	var order RedemptionIssuanceOrder
	result := DB.Where("partner_id = ? AND merchant_order_id = ?", partnerID, merchantOrderID).First(&order)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, "", false, nil
	}
	if result.Error != nil {
		return nil, "", true, result.Error
	}
	var code string
	var terminalErr error
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", order.ID).First(&order).Error; err != nil {
			return err
		}
		if order.ProductCode != productCode {
			return ErrOrderParameterMismatch
		}
		if err := loadExistingPartnerOrder(tx, &order, &code); err != nil {
			if errors.Is(err, ErrOrderExpired) {
				terminalErr = err
				return nil
			}
			return err
		}
		return nil
	})
	if terminalErr != nil {
		return &order, "", true, terminalErr
	}
	return &order, code, true, err
}

func QueryPartnerOrder(partnerID, merchantOrderID string) (*RedemptionIssuanceOrder, string, error) {
	var order RedemptionIssuanceOrder
	var code string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("partner_id = ? AND merchant_order_id = ?", strings.TrimSpace(partnerID), strings.TrimSpace(merchantOrderID)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		if order.Status != RedemptionIssuanceStatusIssued {
			return nil
		}
		if order.ExpiresAt != 0 && order.ExpiresAt <= common.GetTimestamp() {
			return markPartnerOrderExpired(tx, &order)
		}
		var redemption Redemption
		if err := lockForUpdate(tx).First(&redemption, order.RedemptionID).Error; err != nil {
			return err
		}
		if redemption.Status != common.RedemptionCodeStatusEnabled {
			return nil
		}
		if redemption.ExpiredTime != 0 && redemption.ExpiredTime <= common.GetTimestamp() {
			return markPartnerOrderExpired(tx, &order)
		}
		var decryptErr error
		code, decryptErr = decryptRedemptionCode(redemption.KeyCiphertext)
		return decryptErr
	})
	if err != nil {
		return nil, "", err
	}
	return &order, code, nil
}

func RevokePartnerOrder(partnerID, merchantOrderID, reason string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var order RedemptionIssuanceOrder
		if err := lockForUpdate(tx).Where("partner_id = ? AND merchant_order_id = ?", strings.TrimSpace(partnerID), strings.TrimSpace(merchantOrderID)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		if order.Status == RedemptionIssuanceStatusRedeemed {
			return ErrOrderAlreadyRedeemed
		}
		if order.Status == RedemptionIssuanceStatusExpired {
			return ErrOrderExpired
		}
		if order.Status == RedemptionIssuanceStatusRevoked {
			return ErrOrderRevoked
		}
		if order.ExpiresAt != 0 && common.GetTimestamp() >= order.ExpiresAt {
			return ErrOrderExpired
		}
		result := tx.Model(&Redemption{}).Where("id = ? AND status = ?", order.RedemptionID, common.RedemptionCodeStatusEnabled).Update("status", common.RedemptionCodeStatusDisabled)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrderAlreadyRedeemed
		}
		now := common.GetTimestamp()
		return tx.Model(&order).Updates(map[string]any{"status": RedemptionIssuanceStatusRevoked, "revoked_at": now, "revoked_reason": strings.TrimSpace(reason)}).Error
	})
}
