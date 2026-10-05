package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type userConcurrencyResponse struct {
	UserID                int  `json:"user_id"`
	Current               *int `json:"current"`
	MaxConcurrentRequests int  `json:"max_concurrent_requests"`
}

// concurrencyQueryIDs bounds dashboard polling and rejects ambiguous IDs. No
// API keys or credentials are accepted or returned by these monitoring APIs.
func concurrencyQueryIDs(c *gin.Context) ([]int, error) {
	value := c.Query("ids")
	ids := make([]int, 0)
	if value == "" {
		return ids, nil
	}
	seen := make(map[int]bool)
	for part := range strings.SplitSeq(value, ",") {
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 || len(ids) >= 100 {
			return nil, errors.New("invalid concurrency IDs")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}

func GetTokenConcurrency(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	userID := c.GetInt("id")
	if userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "authentication required"})
		return
	}
	ids, err := concurrencyQueryIDs(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if len(ids) > 0 {
		var count int64
		if err = model.DB.Model(&model.Token{}).Where("user_id = ? AND id IN ?", userID, ids).Count(&count).Error; err != nil {
			common.ApiError(c, err)
			return
		}
		if count != int64(len(ids)) {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "token unavailable"})
			return
		}
	}
	limit, err := model.GetUserConcurrencyLimit(userID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	snapshot, err := service.GetRelayConcurrency(c.Request.Context(), userID, ids)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "concurrency store unavailable"})
		return
	}
	common.ApiSuccess(c, struct {
		userConcurrencyResponse
		Tokens map[int]int `json:"tokens"`
	}{userConcurrencyResponse: userConcurrencyResponse{UserID: userID, Current: &snapshot.Current, MaxConcurrentRequests: limit}, Tokens: snapshot.Tokens})
}

func GetUserConcurrency(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetInt("role") < common.RoleAdminUser {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return
	}
	ids, err := concurrencyQueryIDs(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	result := make(map[int]userConcurrencyResponse, len(ids))
	if len(ids) == 0 {
		common.ApiSuccess(c, result)
		return
	}
	var users []model.User
	if err = model.DB.Select("id", "max_concurrent_requests").Where("id IN ?", ids).Find(&users).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	for _, user := range users {
		snapshot, err := service.GetRelayConcurrency(c.Request.Context(), user.Id, nil)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "concurrency store unavailable"})
			return
		}
		result[user.Id] = userConcurrencyResponse{UserID: user.Id, Current: &snapshot.Current, MaxConcurrentRequests: user.MaxConcurrentRequests}
	}
	common.ApiSuccess(c, result)
}

func UpdateUserConcurrency(c *gin.Context) {
	var request struct {
		MaxConcurrentRequests *int `json:"max_concurrent_requests"`
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiError(c, errors.New("invalid user ID"))
		return
	}
	if err = common.DecodeJson(c.Request.Body, &request); err != nil || request.MaxConcurrentRequests == nil || *request.MaxConcurrentRequests < 0 || *request.MaxConcurrentRequests > model.MaxUserConcurrentRequests {
		common.ApiError(c, errors.New("invalid concurrency limit"))
		return
	}
	if err = model.UpdateUserConcurrencyLimit(id, c.GetInt("role"), *request.MaxConcurrentRequests); err != nil {
		if errors.Is(err, model.ErrUserConcurrencyPermission) {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
			return
		}
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, id, "user.concurrency_update", map[string]any{"max_concurrent_requests": *request.MaxConcurrentRequests})
	response := userConcurrencyResponse{UserID: id, MaxConcurrentRequests: *request.MaxConcurrentRequests}
	snapshot, err := service.GetRelayConcurrency(c.Request.Context(), id, nil)
	if err == nil {
		response.Current = &snapshot.Current
	}
	// The cap is already durable. Missing live data must not report a failed save
	// or fabricate a zero count; dashboard polling will retry the snapshot.
	common.ApiSuccess(c, response)
}
