package sub2api

import (
	"io"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/newapi"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

type Adaptor struct {
	newapi.Adaptor
}

// NormalizeBaseURL accepts both the documented host-only base URL and a base
// URL that already ends in /v1.
func NormalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return strings.TrimSuffix(baseURL, "/v1")
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

// GetRequestURL accepts both the documented host-only base URL and a base URL
// that already ends in /v1. Sub2API exposes its compatible endpoints below
// /v1, so keeping one version prefix avoids accidental /v1/v1 requests.
func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := NormalizeBaseURL(info.ChannelBaseUrl)
	requestPath := info.RequestURLPath
	if info.RelayMode == relayconstant.RelayModeAlphaSearch {
		requestPath = "/v1/alpha/search"
	}
	return relaycommon.GetFullRequestURL(baseURL, requestPath, info.ChannelType), nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
