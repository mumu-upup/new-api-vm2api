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
		{http.MethodDelete, "/:id", authz.GPTAccountManage, controller.DeleteGPTAccount},
		{http.MethodPost, "/:id/login", authz.GPTAccountManage, controller.LoginGPTAccount},
		{http.MethodGet, "/:id/oauth", authz.GPTAccountManage, controller.StartGPTAccountOAuth},
		{http.MethodPost, "/:id/callback", authz.GPTAccountManage, controller.CompleteGPTAccountOAuth},
		{http.MethodPost, "/:id/refresh", authz.GPTAccountManage, controller.RefreshGPTAccount},
	} {
		handlePermissionRoute(group, route.method, route.path, route.permission, middleware.CriticalRateLimit(), route.handler)
	}
}
