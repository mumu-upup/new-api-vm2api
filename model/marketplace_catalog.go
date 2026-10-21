package model

import (
	"errors"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MarketplaceCatalogOption = "marketplace_setting.catalog"

type MarketplaceCatalog struct {
	Version int                        `json:"version"`
	Vendors []MarketplaceCatalogVendor `json:"vendors"`
}

type MarketplaceCatalogVendor struct {
	VendorID int      `json:"vendor_id"`
	Region   string   `json:"region"`
	Groups   []string `json:"groups"`
}

func parseMarketplaceCatalog(raw string) (MarketplaceCatalog, error) {
	var catalog MarketplaceCatalog
	if err := common.UnmarshalJsonStr(raw, &catalog); err != nil {
		return catalog, common.NewMessage("Invalid marketplace catalog")
	}
	if catalog.Version != 1 || catalog.Vendors == nil {
		return catalog, common.NewMessage("Marketplace catalog requires version 1 and a vendors array")
	}
	seen := map[int]bool{}
	for _, vendor := range catalog.Vendors {
		if vendor.VendorID <= 0 || seen[vendor.VendorID] {
			return catalog, common.NewMessage("Marketplace catalog requires unique saved vendor IDs")
		}
		seen[vendor.VendorID] = true
		if vendor.Region != "domestic" && vendor.Region != "international" {
			return catalog, common.NewMessage("Invalid marketplace vendor region")
		}
		if vendor.Groups == nil {
			return catalog, common.NewMessage("Marketplace vendor requires a groups array")
		}
		groups := map[string]bool{}
		for _, group := range vendor.Groups {
			if group == "" || groups[group] {
				return catalog, common.NewMessage("Marketplace catalog requires unique nonempty groups")
			}
			groups[group] = true
		}
	}
	return catalog, nil
}

func validateMarketplaceCatalog(tx *gorm.DB, raw string) error {
	catalog, err := parseMarketplaceCatalog(raw)
	if err != nil {
		return err
	}
	for _, vendor := range catalog.Vendors {
		if err := validateModelVendor(tx, vendor.VendorID); err != nil {
			return err
		}
		for _, group := range vendor.Groups {
			if !ratio_setting.ContainsGroupRatio(group) {
				return common.NewMessage("Unknown marketplace group: {{group}}", map[string]any{"group": group})
			}
		}
	}
	return nil
}

func GetMarketplaceCatalog() (MarketplaceCatalog, error) {
	common.OptionMapRWMutex.RLock()
	raw, exists := common.OptionMap[MarketplaceCatalogOption]
	common.OptionMapRWMutex.RUnlock()
	if !exists {
		return MarketplaceCatalog{Version: 1, Vendors: []MarketplaceCatalogVendor{}}, nil
	}
	return parseMarketplaceCatalog(raw)
}

func updateMarketplaceCatalog(raw string) error {
	metadataMutationMu.Lock()
	defer metadataMutationMu.Unlock()
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockMetadataMutation(tx); err != nil {
			return err
		}
		if err := validateMarketplaceCatalog(tx, raw); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&Option{Key: MarketplaceCatalogOption, Value: raw}).Error
	})
	if err != nil {
		return err
	}
	return updateOptionMap(MarketplaceCatalogOption, raw)
}

func rejectMarketplaceVendorRemoval(tx *gorm.DB, ids []int) error {
	var option Option
	err := tx.Where(commonKeyCol+" = ?", MarketplaceCatalogOption).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	catalog, err := parseMarketplaceCatalog(option.Value)
	if err != nil {
		return err
	}
	for _, vendor := range catalog.Vendors {
		if slices.Contains(ids, vendor.VendorID) {
			return common.NewMessage("Vendor is referenced by marketplace catalog; remove its catalog entry before merging or deleting")
		}
	}
	return nil
}
