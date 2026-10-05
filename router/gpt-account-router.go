package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"net/http"
)

func registerGPTAccountRoutes(api *gin.RouterGroup) {
	group := api.Group("/gpt-accounts", middleware.AdminAuth(), middleware.DisableCache())
	for _, route := range []permissionRoute{
		{http.MethodGet, "", authz.GPTAccountRead, controller.ListGPTAccounts},
		{http.MethodGet, "/config", authz.GPTAccountRead, controller.GPTAccountsConfig},
		{http.MethodPost, "", authz.GPTAccountManage, controller.CreateGPTAccount},
		{http.MethodPut, "/:id", authz.GPTAccountManage, controller.UpdateGPTAccount},
		{http.MethodPost, "/:id/totp", authz.GPTAccountManage, controller.UpdateGPTAccountTOTP},
		{http.MethodPost, "/:id/totp/reveal", authz.GPTAccountManage, controller.RevealGPTAccountTOTP},
		{http.MethodDelete, "/:id", authz.GPTAccountManage, controller.DeleteGPTAccount},
		{http.MethodPost, "/:id/login", authz.GPTAccountManage, controller.LoginGPTAccount},
		{http.MethodGet, "/:id/oauth", authz.GPTAccountManage, controller.StartGPTAccountOAuth},
		{http.MethodPost, "/:id/callback", authz.GPTAccountManage, controller.CompleteGPTAccountOAuth},
		{http.MethodPost, "/:id/refresh", authz.GPTAccountManage, controller.RefreshGPTAccount},
		{http.MethodPost, "/:id/sync", authz.GPTAccountManage, controller.SyncGPTAccountGateway},
	} {
		handlePermissionRoute(group, route.method, route.path, route.permission, middleware.CriticalRateLimit(), route.handler)
	}
	gateways := api.Group("/gpt-account-gateways", middleware.AdminAuth(), middleware.DisableCache())
	for _, route := range []permissionRoute{
		{http.MethodGet, "", authz.GPTAccountRead, controller.ListGPTAccountGateways},
		{http.MethodPost, "", authz.GPTAccountManage, controller.CreateGPTAccountGateway},
		{http.MethodPut, "/:id", authz.GPTAccountManage, controller.UpdateGPTAccountGateway},
	} {
		handlePermissionRoute(gateways, route.method, route.path, route.permission, middleware.CriticalRateLimit(), route.handler)
	}
}
