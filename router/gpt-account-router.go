package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"net/http"
)

func registerGPTAccountRoutes(api *gin.RouterGroup) {
	statusChecks := api.Group("/gpt-account-status-checks", middleware.AdminAuth(), middleware.DisableCache())
	for _, route := range []permissionRoute{
		{http.MethodPost, "", authz.GPTAccountManage, controller.CreateGPTAccountStatusCheck},
		{http.MethodPost, "/:id/cancel", authz.GPTAccountManage, controller.CancelGPTAccountStatusCheck},
	} {
		handlePermissionRoute(statusChecks, route.method, route.path, route.permission, middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("gpt-account-status-check"), route.handler)
	}
	// Polling is read-only and must not consume the mutation budget.
	handlePermissionRoute(statusChecks, http.MethodGet, "/:id", authz.GPTAccountManage, controller.GetGPTAccountStatusCheck)
	group := api.Group("/gpt-accounts", middleware.AdminAuth(), middleware.DisableCache())
	// Dashboard polling must not consume the budget shared by login and
	// credential changes. These reads retain the parent API rate limiter.
	handlePermissionRoute(group, http.MethodGet, "", authz.GPTAccountRead, controller.ListGPTAccounts)
	handlePermissionRoute(group, http.MethodGet, "/config", authz.GPTAccountRead, controller.GPTAccountsConfig)
	handlePermissionRoute(group, http.MethodGet, "/:id/vm2api/instance", authz.GPTAccountRead, controller.GetGPTAccountVM2APIInstance)
	handlePermissionRoute(group, http.MethodGet, "/:id/vm2api", authz.GPTAccountRead, controller.GetGPTAccountVM2APIStatus)
	handlePermissionRoute(group, http.MethodGet, "/:id/vm2api/logs", authz.GPTAccountRead, controller.ListGPTAccountVM2APILogs)
	for _, route := range []permissionRoute{
		{http.MethodPost, "/:id/vm2api/instance", authz.GPTAccountManage, controller.CreateGPTAccountVM2APIInstance},
		{http.MethodPost, "/:id/vm2api/instance/:action", authz.GPTAccountManage, controller.ManageGPTAccountVM2APIInstance},
		{http.MethodPost, "", authz.GPTAccountManage, controller.CreateGPTAccount},
		{http.MethodPut, "/config", authz.GPTAccountManage, controller.UpdateGPTAccountsConfig},
		{http.MethodPost, "/import-oauth", authz.GPTAccountManage, controller.ImportGPTAccountOAuth},
		{http.MethodPut, "/:id", authz.GPTAccountManage, controller.UpdateGPTAccount},
		{http.MethodPost, "/:id/totp", authz.GPTAccountManage, controller.UpdateGPTAccountTOTP},
		{http.MethodPost, "/:id/totp/change", authz.GPTAccountManage, controller.ChangeGPTAccountTOTP},
		{http.MethodPost, "/:id/totp/reveal", authz.GPTAccountManage, controller.RevealGPTAccountTOTP},
		{http.MethodDelete, "/:id", authz.GPTAccountManage, controller.DeleteGPTAccount},
		{http.MethodPost, "/:id/login", authz.GPTAccountManage, controller.LoginGPTAccount},
		{http.MethodPost, "/:id/import-oauth", authz.GPTAccountManage, controller.ImportGPTAccountOAuthForAccount},
		{http.MethodGet, "/:id/oauth", authz.GPTAccountManage, controller.StartGPTAccountOAuth},
		{http.MethodPost, "/:id/callback", authz.GPTAccountManage, controller.CompleteGPTAccountOAuth},
		{http.MethodPost, "/:id/refresh", authz.GPTAccountManage, controller.RefreshGPTAccount},
		{http.MethodPost, "/:id/sync", authz.GPTAccountManage, controller.SyncGPTAccountGateway},
		{http.MethodPatch, "/:id/vm2api/policy", authz.GPTAccountManage, controller.UpdateGPTAccountVM2APIPolicy},
		{http.MethodPatch, "/:id/vm2api/schedulable", authz.GPTAccountManage, controller.SetGPTAccountVM2APISchedulable},
		{http.MethodPost, "/:id/vm2api/quota/refresh", authz.GPTAccountManage, controller.RefreshGPTAccountVM2APIQuota},
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
