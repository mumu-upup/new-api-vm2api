package controller

import (
	"maps"
	"math"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		groups := make([]string, 0, len(item.EnableGroup))
		if slices.Contains(item.EnableGroup, "all") {
			for group := range usableGroup {
				groups = append(groups, group)
			}
			slices.Sort(groups)
		} else {
			for _, group := range item.EnableGroup {
				if _, ok := usableGroup[group]; ok {
					groups = append(groups, group)
				}
			}
		}
		if len(groups) > 0 {
			item.EnableGroup = groups
			filtered = append(filtered, item)
		}
	}

	return filtered
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	userId, exists := c.Get("id")
	usableGroup := map[string]string{}
	groupRatio := map[string]float64{}
	maps.Copy(groupRatio, ratio_setting.GetGroupRatioCopy())
	var group string
	if exists {
		user, err := model.GetUserCache(userId.(int))
		if err != nil {
			common.ApiError(c, err)
			return
		}
		group = user.Group
		for g := range groupRatio {
			if ratio, ok := ratio_setting.GetGroupGroupRatio(group, g); ok {
				groupRatio[g] = ratio
			}
		}
	}

	id := 0
	if exists {
		id = userId.(int)
	}
	var err error
	usableGroup, err = service.GetUserUsableGroupsForUser(id, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	for name := range usableGroup {
		ratio, ok := groupRatio[name]
		if !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
			delete(usableGroup, name)
		}
	}
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	// check groupRatio contains usableGroup
	for group := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}

	catalog, err := buildMarketplacePricingCatalog(pricing, usableGroup, groupRatio)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	autoGroups := []string{}
	for _, name := range setting.GetAutoGroups() {
		if _, ok := usableGroup[name]; ok && !slices.Contains(autoGroups, name) {
			autoGroups = append(autoGroups, name)
		}
	}
	vendors := []model.PricingVendor{}
	for _, vendor := range model.GetVendors() {
		if slices.ContainsFunc(pricing, func(item model.Pricing) bool { return item.VendorID == vendor.ID }) {
			vendors = append(vendors, vendor)
		}
	}
	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            vendors,
		"catalog":            catalog,
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        autoGroups,
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
