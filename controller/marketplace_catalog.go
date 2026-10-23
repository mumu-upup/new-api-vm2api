package controller

import (
	"github.com/QuantumNous/new-api/model"
)

type marketplacePricingGroup struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Ratio       float64 `json:"ratio"`
	ModelCount  int     `json:"model_count"`
}

type marketplacePricingVendor struct {
	VendorID    int                       `json:"vendor_id"`
	Name        string                    `json:"name"`
	Icon        string                    `json:"icon,omitempty"`
	Description string                    `json:"description,omitempty"`
	Region      string                    `json:"region"`
	Groups      []marketplacePricingGroup `json:"groups"`
}

func buildMarketplacePricingCatalog(pricing []model.Pricing, usable map[string]string, ratios map[string]float64) ([]marketplacePricingVendor, error) {
	catalog, err := model.GetMarketplaceCatalog()
	if err != nil {
		return nil, err
	}
	result := []marketplacePricingVendor{}
	if len(catalog.Vendors) == 0 {
		return result, nil
	}
	var vendors []model.Vendor
	if err := model.DB.Find(&vendors).Error; err != nil {
		return nil, err
	}
	byID := map[int]model.Vendor{}
	for _, vendor := range vendors {
		byID[vendor.Id] = vendor
	}
	for _, entry := range catalog.Vendors {
		vendor, ok := byID[entry.VendorID]
		if !ok {
			continue
		}
		row := marketplacePricingVendor{VendorID: vendor.Id, Name: vendor.Name, Icon: vendor.Icon, Description: vendor.Description, Region: entry.Region, Groups: []marketplacePricingGroup{}}
		for _, name := range entry.Groups {
			description, ok := usable[name]
			if !ok {
				continue
			}
			ratio, ok := ratios[name]
			if !ok {
				continue
			}
			count := 0
			for _, item := range pricing {
				if item.VendorID != vendor.Id {
					continue
				}
				for _, group := range item.EnableGroup {
					if group == name {
						count++
						break
					}
				}
			}
			row.Groups = append(row.Groups, marketplacePricingGroup{Name: name, Description: description, Ratio: ratio, ModelCount: count})
		}
		if len(row.Groups) > 0 {
			result = append(result, row)
		}
	}
	return result, nil
}
