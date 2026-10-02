package controller

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const partnerBodyLimit = 64 << 10

type partnerIssueRequest struct {
	MerchantOrderID string `json:"merchant_order_id"`
	ProductCode     string `json:"product_code"`
}
type partnerQueryRequest struct {
	MerchantOrderID string `json:"merchant_order_id"`
}
type partnerRevokeRequest struct {
	MerchantOrderID string `json:"merchant_order_id"`
	Reason          string `json:"reason"`
}

// readPartnerJSON authenticates the exact bytes sent by the partner before
// decoding them. This prevents a signature being checked against a different
// representation than the handler consumes.
func readPartnerJSON(c *gin.Context, dst any) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		partnerError(c, http.StatusBadRequest, "invalid_request")
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, partnerBodyLimit+1))
	if err != nil || len(body) == 0 || len(body) > partnerBodyLimit {
		partnerError(c, http.StatusBadRequest, "invalid_request")
		return "", false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	req := service.PartnerRequest{
		Context: c.Request.Context(), PartnerID: c.GetHeader("X-Partner-Id"),
		Timestamp: parsePartnerTimestamp(c.GetHeader("X-Timestamp")), Nonce: c.GetHeader("X-Nonce"),
		Signature: c.GetHeader("X-Signature"), Method: c.Request.Method, Path: c.Request.URL.Path,
		Body: body, ClientIP: net.ParseIP(c.ClientIP()),
	}
	partner, authErr := service.VerifyPartnerRequest(c.Request.Context(), req)
	if authErr != nil {
		partnerError(c, partnerStatus(authErr), partnerCode(authErr))
		return "", false
	}
	if err := c.ShouldBindJSON(dst); err != nil {
		partnerError(c, http.StatusBadRequest, "invalid_request")
		return "", false
	}
	return partner.PartnerID, true
}

func parsePartnerTimestamp(value string) int64 {
	var n int64
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int64(r-'0')
		if n > 1<<62 {
			return 0
		}
	}
	return n
}

func partnerError(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code}})
}
func partnerCode(err error) string {
	switch {
	case errors.Is(err, model.ErrProductNotFound):
		return "invalid_product"
	case errors.Is(err, model.ErrOrderParameterMismatch):
		return "order_conflict"
	case errors.Is(err, model.ErrOrderNotFound):
		return "order_not_found"
	case errors.Is(err, model.ErrOrderAlreadyRedeemed):
		return "order_redeemed"
	case errors.Is(err, model.ErrOrderExpired):
		return "order_expired"
	case errors.Is(err, model.ErrOrderRevoked):
		return "order_revoked"
	case errors.Is(err, service.ErrRequestExpired):
		return "request_expired"
	case errors.Is(err, service.ErrNonceReplay):
		return "nonce_replay"
	case errors.Is(err, service.ErrIPNotAllowed):
		return "ip_not_allowed"
	case errors.Is(err, service.ErrPartnerUnauthorized):
		return "unauthorized"
	case errors.Is(err, service.ErrInvalidSignature):
		return "invalid_signature"
	case errors.Is(err, service.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, service.ErrPartnerRateLimited):
		return "rate_limited"
	default:
		return "internal_error"
	}
}
func partnerStatus(err error) int {
	switch {
	case errors.Is(err, model.ErrOrderParameterMismatch):
		return http.StatusConflict
	case errors.Is(err, service.ErrPartnerUnauthorized), errors.Is(err, service.ErrIPNotAllowed):
		return http.StatusForbidden
	case errors.Is(err, service.ErrRequestExpired), errors.Is(err, service.ErrNonceReplay):
		return http.StatusUnauthorized
	case errors.Is(err, service.ErrInvalidSignature):
		return http.StatusUnauthorized
	case errors.Is(err, service.ErrPartnerRateLimited):
		return http.StatusTooManyRequests
	default:
		return http.StatusBadRequest
	}
}

func PartnerIssueRedemption(c *gin.Context) {
	var req partnerIssueRequest
	partnerID, ok := readPartnerJSON(c, &req)
	if !ok {
		return
	}
	if len(strings.TrimSpace(req.MerchantOrderID)) == 0 || len(req.MerchantOrderID) > 128 || len(strings.TrimSpace(req.ProductCode)) == 0 || len(req.ProductCode) > 32 {
		partnerError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	order, code, err := model.IssuePartnerRedemption(partnerID, strings.TrimSpace(req.MerchantOrderID), strings.TrimSpace(req.ProductCode))
	if err != nil {
		partnerError(c, partnerStatus(err), partnerCode(err))
		return
	}
	partnerOrderResponse(c, order, code)
}

func PartnerQueryRedemption(c *gin.Context) {
	var req partnerQueryRequest
	partnerID, ok := readPartnerJSON(c, &req)
	if !ok {
		return
	}
	if len(strings.TrimSpace(req.MerchantOrderID)) == 0 || len(req.MerchantOrderID) > 128 {
		partnerError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	order, code, err := model.QueryPartnerOrder(partnerID, strings.TrimSpace(req.MerchantOrderID))
	if err != nil {
		partnerError(c, partnerStatus(err), partnerCode(err))
		return
	}
	partnerOrderResponse(c, order, code)
}

func PartnerRevokeRedemption(c *gin.Context) {
	var req partnerRevokeRequest
	partnerID, ok := readPartnerJSON(c, &req)
	if !ok {
		return
	}
	if len(strings.TrimSpace(req.MerchantOrderID)) == 0 || len(req.MerchantOrderID) > 128 || len(req.Reason) > 255 {
		partnerError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := model.RevokePartnerOrder(partnerID, strings.TrimSpace(req.MerchantOrderID), strings.TrimSpace(req.Reason)); err != nil {
		partnerError(c, partnerStatus(err), partnerCode(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}

func partnerOrderResponse(c *gin.Context, order *model.RedemptionIssuanceOrder, code string) {
	if order == nil {
		partnerError(c, http.StatusInternalServerError, "internal_error")
		return
	}
	response := gin.H{"order_id": order.ID, "merchant_order_id": order.MerchantOrderID, "product_code": order.ProductCode, "amount": order.Amount, "quota": order.Quota, "status": order.Status, "issued_at": order.IssuedAt, "expires_at": order.ExpiresAt}
	if order.Status == model.RedemptionIssuanceStatusIssued && code != "" {
		response["code"] = code
	}
	c.JSON(http.StatusOK, response)
}
