package common

import "strings"

// ResponseModel records upstream declarations before response conversion. It is
// diagnostic only: it must never change routing, pricing, or downstream output.
// Only the three names are stored; whether they disagree is computed on demand
// so every consumer applies the current comparison rule to old rows as well.
type ResponseModel struct {
	RequestedModel string `json:"requested_model"`
	UpstreamModel  string `json:"upstream_model"`
	ReturnedModel  string `json:"returned_model"`
}

// matches reports whether an upstream declaration is compatible with the
// requested or upstream model. Date-suffixed model IDs are compatible, while
// quality variants such as "-mini" or "-nano" are deliberately distinct.
func (r *ResponseModel) matches(model string) bool {
	returned := strings.ToLower(strings.TrimSpace(model))
	for _, expected := range []string{r.RequestedModel, r.UpstreamModel} {
		expected = strings.ToLower(strings.TrimSpace(expected))
		if expected == "" {
			continue
		}
		if returned == expected || strings.HasSuffix(returned, expected) {
			return true
		}
		if strings.HasPrefix(returned, expected+"-") && isDateModelSuffix(strings.TrimPrefix(returned, expected)) {
			return true
		}
	}
	return false
}

func isDateModelSuffix(suffix string) bool {
	suffix = strings.TrimPrefix(suffix, "-")
	if len(suffix) < 8 {
		return false
	}
	if len(suffix) >= 10 && suffix[4] == '-' && suffix[7] == '-' {
		for _, index := range []int{0, 1, 2, 3, 5, 6, 8, 9} {
			if suffix[index] < '0' || suffix[index] > '9' {
				return false
			}
		}
		return len(suffix) == 10 || suffix[10] == '-'
	}
	for index := range 8 {
		if suffix[index] < '0' || suffix[index] > '9' {
			return false
		}
	}
	return len(suffix) == 8 || suffix[8] == '-'
}

// Mismatch reports whether the retained upstream declaration disagrees with
// both the requested and upstream models.
func (r *ResponseModel) Mismatch() bool {
	return r != nil && r.ReturnedModel != "" && !r.matches(r.ReturnedModel)
}

// ObserveResponseModel retains the first differing model for inspection, with
// mismatches taking priority over provider-path, prefix, or case-only
// differences. A later matching or empty event cannot erase it. Only observe
// upstream declarations, never models synthesized by a response converter.
func (info *RelayInfo) ObserveResponseModel(model string) {
	if info == nil || strings.TrimSpace(model) == "" {
		return
	}
	if info.ResponseModel == nil {
		info.ResponseModel = &ResponseModel{
			RequestedModel: info.OriginModelName,
			UpstreamModel:  info.GetUpstreamModelName(),
		}
	}
	observation := info.ResponseModel
	if observation.Mismatch() {
		return
	}
	if observation.matches(model) && observation.ReturnedModel != "" &&
		observation.ReturnedModel != observation.RequestedModel && observation.ReturnedModel != observation.UpstreamModel {
		return
	}
	observation.ReturnedModel = model
}
