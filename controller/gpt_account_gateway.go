package controller

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ListGPTAccountGateways(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, false); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	var gateways []model.GPTAccountGateway
	if model.DB.Where("managed_account_id IS NULL").Order("id ASC").Find(&gateways).Error != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	ids := make([]int, 0, len(gateways))
	for _, gateway := range gateways {
		if gateway.ChannelID != nil {
			ids = append(ids, *gateway.ChannelID)
		}
	}
	names, err := model.GetChannelNamesByIds(ids)
	if err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	for i := range gateways {
		if gateways[i].ChannelID != nil {
			gateways[i].ChannelName = names[*gateways[i].ChannelID]
		}
	}
	if c.GetInt("role") != common.RoleRootUser {
		targets := make([]map[string]any, 0, len(gateways))
		for _, gateway := range gateways {
			targets = append(targets, map[string]any{"id": gateway.ID, "name": gateway.Name, "provider": gateway.Provider, "channel_id": gateway.ChannelID, "channel_name": gateway.ChannelName})
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": targets}})
		return
	}
	if gateways == nil {
		gateways = []model.GPTAccountGateway{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": gateways}})
}

func CreateGPTAccountGateway(c *gin.Context) { saveGPTAccountGateway(c, false) }
func UpdateGPTAccountGateway(c *gin.Context) { saveGPTAccountGateway(c, true) }

func ImportGPTGatewayChannelJSON(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	if c.GetInt("role") != common.RoleRootUser {
		gptAccountError(c, http.StatusForbidden, "authorization_forbidden")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var request struct {
		ConfigJSON string `json:"config_json"`
	}
	if c.ShouldBindJSON(&request) != nil {
		gptAccountError(c, http.StatusBadRequest, "gateway_channel_json_invalid")
		return
	}
	input, err := service.ParseGPTGatewayChannelJSON(request.ConfigJSON)
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, service.GPTAccountErrorCode(err))
		return
	}
	channel, err := service.CreateGPTGatewayChannel(input)
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, "gateway_channel_create_failed")
		return
	}
	recordManageAudit(c, "gpt_account_gateway.channel.create", map[string]any{"channel_id": channel.Id, "provider": input.Provider})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"channel_id": channel.Id, "name": channel.Name, "provider": input.Provider}})
}

func saveGPTAccountGateway(c *gin.Context, update bool) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	if c.GetInt("role") != common.RoleRootUser {
		gptAccountError(c, http.StatusForbidden, "authorization_forbidden")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input service.GPTAccountGatewayInput
	if c.ShouldBindJSON(&input) != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_gateway")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	scope := "gpt_account_gateway.create"
	var existing *model.GPTAccountGateway
	if update {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			gptAccountError(c, http.StatusBadRequest, "invalid_gateway")
			return
		}
		existing = &model.GPTAccountGateway{}
		if model.DB.First(existing, id).Error != nil {
			gptAccountError(c, http.StatusNotFound, "gateway_not_found")
			return
		}
		scope = "gpt_account_gateway.update"
	}
	gateway, err := service.SaveGPTAccountGateway(existing, input)
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, scope, map[string]any{"gateway_id": gateway.ID, "provider": gateway.Provider})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gateway})
}

func SyncGPTAccountGateway(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	if account.GatewayID == nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_gateway")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	if err := service.SyncGPTAccountGateway(ctx, account.ID); err != nil {
		gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.gateway.sync", map[string]any{"account_id": account.ID, "gateway_id": *account.GatewayID})
	gptAccountResponse(c, account.ID)
}
