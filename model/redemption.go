package model

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"gorm.io/gorm"
)

type Redemption struct {
	Id              int            `json:"id"`
	UserId          int            `json:"user_id"`
	Key             string         `json:"key" gorm:"type:varchar(64);uniqueIndex"`
	KeyHash         *string        `json:"-" gorm:"type:char(43);uniqueIndex"`
	KeyCiphertext   string         `json:"-" gorm:"type:text"`
	Source          string         `json:"source" gorm:"type:varchar(32);index"`
	IssuanceOrderID *uint          `json:"issuance_order_id" gorm:"index"`
	Status          int            `json:"status" gorm:"default:1"`
	Name            string         `json:"name" gorm:"index"`
	Quota           int            `json:"quota" gorm:"default:100"`
	CreatedTime     int64          `json:"created_time" gorm:"bigint"`
	RedeemedTime    int64          `json:"redeemed_time" gorm:"bigint"`
	Count           int            `json:"count" gorm:"-:all"` // only for api request
	UsedUserId      int            `json:"used_user_id"`
	DeletedAt       gorm.DeletedAt `gorm:"index"`
	ExpiredTime     int64          `json:"expired_time" gorm:"bigint"` // 过期时间，0 表示不过期
}

func GetAllRedemptions(startIdx int, num int) (redemptions []*Redemption, total int64, err error) {
	// 开始事务
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 获取总数
	err = tx.Model(&Redemption{}).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 获取分页数据
	err = tx.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 提交事务
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return redemptions, total, nil
}

func SearchRedemptions(keyword string, status string, startIdx int, num int) (redemptions []*Redemption, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&Redemption{})

	if keyword != "" {
		if id, err := strconv.Atoi(keyword); err == nil {
			query = query.Where("id = ? OR name LIKE ?", id, keyword+"%")
		} else {
			query = query.Where("name LIKE ?", keyword+"%")
		}
	}

	if status != "" {
		now := common.GetTimestamp()
		switch status {
		case "expired":
			query = query.Where(
				"status = ? AND expired_time != 0 AND expired_time < ?",
				common.RedemptionCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.RedemptionCodeStatusEnabled):
			query = query.Where(
				"status = ? AND (expired_time = 0 OR expired_time >= ?)",
				common.RedemptionCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.RedemptionCodeStatusDisabled):
			query = query.Where("status = ?", common.RedemptionCodeStatusDisabled)
		case strconv.Itoa(common.RedemptionCodeStatusUsed):
			query = query.Where("status = ?", common.RedemptionCodeStatusUsed)
		}
	}

	// Get total count
	err = query.Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated data
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return redemptions, total, nil
}

func GetRedemptionById(id int) (*Redemption, error) {
	if id == 0 {
		return nil, common.NewMessage("ID is empty")
	}
	redemption := Redemption{Id: id}
	var err error = nil
	err = DB.First(&redemption, "id = ?", id).Error
	return &redemption, err
}

func Redeem(key string, userId int) (quota int, err error) {
	if key == "" {
		return 0, errors.New("redemption code not provided")
	}
	if userId == 0 {
		return 0, errors.New("invalid user id")
	}
	redemption := &Redemption{}

	keyHash := hashRedemptionCode(key)
	common.RandomSleep()
	err = DB.Transaction(func(tx *gorm.DB) error {
		// Read the candidate without a lock first so partner orders can be
		// locked before their redemption row. Revoke uses the same order-first
		// sequence, avoiding a lock inversion between redeem and revoke.
		err := tx.Where("key_hash = ?", keyHash).First(redemption).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			keyCol := "`key`"
			if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
				keyCol = `"key"`
			}
			err = tx.Where(keyCol+" = ?", key).First(redemption).Error
		}
		if err != nil {
			return errors.New("invalid redemption code")
		}
		var issuanceOrder *RedemptionIssuanceOrder
		if redemption.IssuanceOrderID != nil {
			issuanceOrder = &RedemptionIssuanceOrder{}
			if err := lockForUpdate(tx).First(issuanceOrder, *redemption.IssuanceOrderID).Error; err != nil {
				return err
			}
			if issuanceOrder.Status == RedemptionIssuanceStatusRevoked {
				return ErrOrderRevoked
			}
			if issuanceOrder.Status == RedemptionIssuanceStatusExpired || (issuanceOrder.ExpiresAt != 0 && issuanceOrder.ExpiresAt < common.GetTimestamp()) {
				return ErrOrderExpired
			}
			if issuanceOrder.Status != RedemptionIssuanceStatusIssued {
				return ErrOrderAlreadyRedeemed
			}
		}
		if err := lockRedemptionForUpdate(tx, redemption); err != nil {
			return err
		}
		if redemption.Status != common.RedemptionCodeStatusEnabled {
			return errors.New("redemption code already used")
		}
		if redemption.ExpiredTime != 0 && redemption.ExpiredTime < common.GetTimestamp() {
			return errors.New("redemption code expired")
		}
		// Compare-and-swap on status: only the transaction that flips
		// enabled -> used may credit quota, so a concurrent redeem of the
		// same code loses here even without a row lock (e.g. on SQLite).
		result := tx.Model(&Redemption{}).
			Where("id = ? AND status = ?", redemption.Id, common.RedemptionCodeStatusEnabled).
			Updates(map[string]interface{}{
				"redeemed_time": common.GetTimestamp(),
				"status":        common.RedemptionCodeStatusUsed,
				"used_user_id":  userId,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("redemption code already used")
		}
		if issuanceOrder != nil {
			if updated := tx.Model(&RedemptionIssuanceOrder{}).Where("id = ? AND status = ?", issuanceOrder.ID, RedemptionIssuanceStatusIssued).Updates(map[string]interface{}{"status": RedemptionIssuanceStatusRedeemed, "redeemed_at": common.GetTimestamp()}); updated.Error != nil {
				return updated.Error
			} else if updated.RowsAffected != 1 {
				return ErrOrderAlreadyRedeemed
			}
		}
		return tx.Model(&User{}).Where("id = ?", userId).Update("quota", gorm.Expr("quota + ?", redemption.Quota)).Error
	})
	if err != nil {
		common.SysError(common.LogText("redemption failed: %s", err.Error()))
		return 0, ErrRedeemFailed
	}
	syncCreditUserQuotaCache(userId, redemption.Quota, common.LogText("redemption"))
	RecordLog(userId, LogTypeTopup, common.NewMessage("Topped up {{quota}} with a redemption code, code ID {{id}}", map[string]any{"quota": logger.FormatQuota(redemption.Quota), "id": redemption.Id}))
	return redemption.Quota, nil
}

func lockRedemptionForUpdate(tx *gorm.DB, redemption *Redemption) error {
	return lockForUpdate(tx).First(redemption, redemption.Id).Error
}

func (redemption *Redemption) Insert() error {
	var err error
	err = DB.Create(redemption).Error
	return err
}

func (redemption *Redemption) SelectUpdate() error {
	// This can update zero values
	return DB.Model(redemption).Select("redeemed_time", "status").Updates(redemption).Error
}

// Update Make sure your token's fields is completed, because this will update non-zero values
func (redemption *Redemption) Update() error {
	var err error
	err = DB.Model(redemption).Select("name", "status", "quota", "redeemed_time", "expired_time").Updates(redemption).Error
	return err
}

func (redemption *Redemption) Delete() error {
	var err error
	err = DB.Delete(redemption).Error
	return err
}

func DeleteRedemptionById(id int) (err error) {
	if id == 0 {
		return common.NewMessage("ID is empty")
	}
	redemption := Redemption{Id: id}
	err = DB.Where(redemption).First(&redemption).Error
	if err != nil {
		return err
	}
	return redemption.Delete()
}

func DeleteInvalidRedemptions() (int64, error) {
	now := common.GetTimestamp()
	result := DB.Where("status IN ? OR (status = ? AND expired_time != 0 AND expired_time < ?)", []int{common.RedemptionCodeStatusUsed, common.RedemptionCodeStatusDisabled}, common.RedemptionCodeStatusEnabled, now).Delete(&Redemption{})
	return result.RowsAffected, result.Error
}
