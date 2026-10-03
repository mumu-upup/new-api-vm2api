package common

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// ResponseModel records upstream declarations before response conversion. It is
// diagnostic only: it must never change routing, pricing, or downstream output.
// Missing declarations remain unknown; these fields do not measure answer quality.
type ResponseModel struct {
	RequestedModel         string `json:"requested_model"`
	UpstreamModel          string `json:"upstream_model"`
	ReturnedModel          string `json:"returned_model"`
	ActualModel            string `json:"actual_model,omitempty"`
	ReportedRequestedModel string `json:"reported_requested_model,omitempty"`
	Degraded               *bool  `json:"degraded,omitempty"`
	DegradeReason          string `json:"degrade_reason,omitempty"`
	DeclarationSource      string `json:"declaration_source,omitempty"`
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
	return r.MismatchModel() != ""
}

// MismatchModel returns the differing declared model, preferring an explicit
// actual model over the protocol's model field. A degradation flag alone does
// not establish a model-name mismatch.
func (r *ResponseModel) MismatchModel() string {
	if r == nil {
		return ""
	}
	for _, model := range []string{r.ActualModel, r.ReturnedModel} {
		if strings.TrimSpace(model) != "" && !r.matches(model) {
			return model
		}
	}
	return ""
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
	if observation.ReturnedModel != "" && !observation.matches(observation.ReturnedModel) {
		return
	}
	if observation.matches(model) && observation.ReturnedModel != "" &&
		observation.ReturnedModel != observation.RequestedModel && observation.ReturnedModel != observation.UpstreamModel {
		return
	}
	observation.ReturnedModel = model
}

// ObserveResponseModelHeaders reads only headers received from the upstream.
// Client-supplied headers must never be used as model evidence.
func (info *RelayInfo) ObserveResponseModelHeaders(headers http.Header) {
	for _, name := range []string{"OpenAI-Model", "X-OpenAI-Model", "X-Served-Model", "X-Model-Name", "X-Model", "Model"} {
		if model := headers.Get(name); model != "" {
			info.ObserveResponseModel(model)
			break
		}
	}
	var degraded *bool
	switch strings.ToLower(strings.TrimSpace(headers.Get("X-Model-Degraded"))) {
	case "true", "1":
		value := true
		degraded = &value
	case "false", "0":
		value := false
		degraded = &value
	}
	info.observeModelDeclaration(headers.Get("X-Actual-Model"), headers.Get("X-Requested-Model"), degraded, headers.Get("X-Model-Degrade-Reason"), "upstream_header")
}

// ObserveModelDiagnostics accepts optional gateway declarations in JSON or SSE.
// It deliberately does not traverse generated content, tool arguments or metadata.
func (info *RelayInfo) ObserveModelDiagnostics(data string) {
	if !strings.Contains(data, `"actual_model"`) && !strings.Contains(data, `"model_degraded"`) &&
		!strings.Contains(data, `"model_degrade_reason"`) && !strings.Contains(data, `"requested_model"`) {
		return
	}
	if !gjson.Valid(data) {
		return
	}
	root := gjson.Parse(data)
	for _, envelope := range []gjson.Result{root, root.Get("response"), root.Get("message")} {
		var degraded *bool
		flag := envelope.Get("model_degraded")
		if flag.Type == gjson.True || flag.Type == gjson.False {
			value := flag.Bool()
			degraded = &value
		}
		info.observeModelDeclaration(envelope.Get("actual_model").Str, envelope.Get("requested_model").Str, degraded, envelope.Get("model_degrade_reason").Str, "upstream_body")
	}
}

func (info *RelayInfo) observeModelDeclaration(actual, requested string, degraded *bool, reason, source string) {
	if info == nil {
		return
	}
	actual, requested, reason = strings.TrimSpace(actual), strings.TrimSpace(requested), strings.TrimSpace(reason)
	if actual == "" && requested == "" && degraded == nil && reason == "" {
		return
	}
	if info.ResponseModel == nil {
		info.ResponseModel = &ResponseModel{RequestedModel: info.OriginModelName, UpstreamModel: info.GetUpstreamModelName()}
	}
	observation := info.ResponseModel
	// Retain the first alert and its context across later stream frames. A later
	// downgrade may supersede a matching or negative declaration.
	hasAlert := (observation.Degraded != nil && *observation.Degraded) ||
		(observation.ActualModel != "" && !observation.matches(observation.ActualModel))
	incomingAlert := (degraded != nil && *degraded) || (actual != "" && !observation.matches(actual))
	updated := false
	replace := incomingAlert && !hasAlert
	actualMismatch := observation.ActualModel != "" && !observation.matches(observation.ActualModel)
	incomingActualMismatch := actual != "" && !observation.matches(actual)
	if actual != "" && (observation.ActualModel == "" || (!actualMismatch && incomingActualMismatch)) {
		updated = true
		observation.ActualModel = string([]rune(actual)[:min(len([]rune(actual)), 256)])
	}
	if requested != "" && (observation.ReportedRequestedModel == "" || replace) {
		updated = true
		observation.ReportedRequestedModel = string([]rune(requested)[:min(len([]rune(requested)), 256)])
	}
	if degraded != nil && (observation.Degraded == nil || !*observation.Degraded) {
		updated = observation.Degraded == nil || *observation.Degraded != *degraded || updated
		observation.Degraded = degraded
	}
	if replace {
		observation.DegradeReason = ""
	}
	if reason != "" && observation.DegradeReason == "" {
		updated = true
		observation.DegradeReason = string([]rune(reason)[:min(len([]rune(reason)), 512)])
	}
	if updated {
		if observation.DeclarationSource == "" {
			observation.DeclarationSource = source
		} else if observation.DeclarationSource != source {
			observation.DeclarationSource = "upstream_header_and_body"
		}
	}
}
