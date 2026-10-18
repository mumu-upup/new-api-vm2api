package controller

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func vm2apiContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 20*time.Second)
}

func GetGPTAccountVM2APIStatus(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, false); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	ctx, cancel := vm2apiContext(c)
	defer cancel()
	status, err := service.GetGPTAccountVM2APIStatus(ctx, account.ID)
	if err != nil {
		gptAccountError(c, http.StatusBadGateway, service.GPTAccountErrorCode(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": status})
}

func UpdateGPTAccountVM2APIPolicy(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	var body map[string]any
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	if c.ShouldBindJSON(&body) != nil || len(body) == 0 {
		gptAccountError(c, http.StatusBadRequest, "invalid_gateway")
		return
	}
	ctx, cancel := vm2apiContext(c)
	defer cancel()
	if err := service.UpdateGPTAccountVM2APIPolicy(ctx, account.ID, body); err != nil {
		gptAccountError(c, http.StatusBadGateway, service.GPTAccountErrorCode(err))
		return
	}
	GetGPTAccountVM2APIStatus(c)
}

func SetGPTAccountVM2APISchedulable(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	var body struct {
		Schedulable *bool `json:"schedulable"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<10)
	if c.ShouldBindJSON(&body) != nil || body.Schedulable == nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_gateway")
		return
	}
	ctx, cancel := vm2apiContext(c)
	defer cancel()
	if err := service.SetGPTAccountVM2APISchedulable(ctx, account.ID, *body.Schedulable); err != nil {
		gptAccountError(c, http.StatusBadGateway, service.GPTAccountErrorCode(err))
		return
	}
	GetGPTAccountVM2APIStatus(c)
}

func RefreshGPTAccountVM2APIQuota(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	ctx, cancel := vm2apiContext(c)
	defer cancel()
	if err := service.RefreshGPTAccountVM2APIQuota(ctx, account.ID); err != nil {
		gptAccountError(c, http.StatusBadGateway, service.GPTAccountErrorCode(err))
		return
	}
	GetGPTAccountVM2APIStatus(c)
}

func ListGPTAccountVM2APILogs(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, false); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	ctx, cancel := vm2apiContext(c)
	defer cancel()
	logs, err := service.ListGPTAccountVM2APILogs(ctx, account.ID, limit)
	if err != nil {
		gptAccountError(c, http.StatusBadGateway, service.GPTAccountErrorCode(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": logs})
}
