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
	// Dashboard polling must not consume the budget shared by login and
	// credential changes. These reads retain the parent API rate limiter.
	handlePermissionRoute(group, http.MethodGet, "", authz.GPTAccountRead, controller.ListGPTAccounts)
	handlePermissionRoute(group, http.MethodGet, "/config", authz.GPTAccountRead, controller.GPTAccountsConfig)
	for _, route := range []permissionRoute{
		{http.MethodPost, "", authz.GPTAccountManage, controller.CreateGPTAccount},
		{http.MethodPost, "/import-oauth", authz.GPTAccountManage, controller.ImportGPTAccountOAuth},
		{http.MethodPut, "/:id", authz.GPTAccountManage, controller.UpdateGPTAccount},
		{http.MethodPost, "/:id/totp", authz.GPTAccountManage, controller.UpdateGPTAccountTOTP},
		{http.MethodPost, "/:id/totp/reveal", authz.GPTAccountManage, controller.RevealGPTAccountTOTP},
		{http.MethodDelete, "/:id", authz.GPTAccountManage, controller.DeleteGPTAccount},
		{http.MethodPost, "/:id/login", authz.GPTAccountManage, controller.LoginGPTAccount},
		{http.MethodPost, "/:id/import-oauth", authz.GPTAccountManage, controller.ImportGPTAccountOAuthForAccount},
		{http.MethodGet, "/:id/oauth", authz.GPTAccountManage, controller.StartGPTAccountOAuth},
		{http.MethodPost, "/:id/callback", authz.GPTAccountManage, controller.CompleteGPTAccountOAuth},
		{http.MethodPost, "/:id/refresh", authz.GPTAccountManage, controller.RefreshGPTAccount},
		{http.MethodPost, "/:id/sync", authz.GPTAccountManage, controller.SyncGPTAccountGateway},
	} {
		handlePermissionRoute(group, route.method, route.path, route.permission, middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("gpt-account"), route.handler)
	}
	gateways := api.Group("/gpt-account-gateways", middleware.AdminAuth(), middleware.DisableCache())
	handlePermissionRoute(gateways, http.MethodGet, "", authz.GPTAccountRead, controller.ListGPTAccountGateways)
	for _, route := range []permissionRoute{
		{http.MethodPost, "", authz.GPTAccountManage, controller.CreateGPTAccountGateway},
		{http.MethodPost, "/import-channel-json", authz.GPTAccountManage, controller.ImportGPTGatewayChannelJSON},
		{http.MethodPut, "/:id", authz.GPTAccountManage, controller.UpdateGPTAccountGateway},
	} {
		handlePermissionRoute(gateways, route.method, route.path, route.permission, middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("gpt-account"), route.handler)
	}
}
