package model

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type RedemptionProduct struct {
	ID              uint      `json:"id"`
	ProductCode     string    `json:"product_code" gorm:"uniqueIndex;type:varchar(32)"`
	Amount          int64     `json:"amount"`
	Quota           int       `json:"quota"`
	ValiditySeconds int64     `json:"validity_seconds"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

var redemptionProductCatalog = []struct {
	Code   string
	Amount int64
}{
	{Code: "CNY_10", Amount: 10},
	{Code: "CNY_50", Amount: 50},
	{Code: "CNY_100", Amount: 100},
	{Code: "CNY_200", Amount: 200},
	{Code: "CNY_500", Amount: 500},
}

func SeedRedemptionProducts() error {
	for _, item := range redemptionProductCatalog {
		quota, err := common.QuotaFromFloatStrict(float64(item.Amount) * common.QuotaPerUnit)
		if err != nil {
			return err
		}
		product := &RedemptionProduct{}
		result := DB.Where("product_code = ?", item.Code).First(product)
		if result.Error == nil {
			continue
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if err := DB.Create(&RedemptionProduct{ProductCode: item.Code, Amount: item.Amount, Quota: quota, ValiditySeconds: 365 * 24 * 60 * 60, Enabled: true}).Error; err != nil {
			return err
		}
	}
	return nil
}

func GetEnabledRedemptionProduct(code string) (*RedemptionProduct, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, ErrProductNotFound
	}
	allowed := false
	for _, item := range redemptionProductCatalog {
		if item.Code == code {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, ErrProductNotFound
	}
	product := &RedemptionProduct{}
	if err := DB.Where("product_code = ? AND enabled = ?", code, true).First(product).Error; err != nil {
		return nil, ErrProductNotFound
	}
	return product, nil
}
