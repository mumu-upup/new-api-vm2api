package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const gptAccountStatusCheckMaxBody = 256 << 10

type gptAccountStatusCheckRequest struct {
	Credentials string                              `json:"credentials"`
	ModelTest   *service.GPTAccountModelTestOptions `json:"model_test"`
}

func CreateGPTAccountStatusCheck(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, gptAccountStatusCheckMaxBody)
	var request gptAccountStatusCheckRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		if common.IsRequestBodyTooLargeError(err) {
			gptAccountError(c, http.StatusRequestEntityTooLarge, "request_body_too_large")
			return
		}
		gptAccountError(c, http.StatusBadRequest, "invalid_credentials")
		return
	}
	credentials, err := service.ParseGPTAccountStatusCheckLines(request.Credentials)
	request.Credentials = ""
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_credentials")
		return
	}
	view, err := service.CreateGPTAccountStatusCheck(identity.UserID, identity, credentials, request.ModelTest)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_request"
		if err.Error() == "job_active" || err.Error() == "job_limit" {
			status, code = http.StatusConflict, err.Error()
		}
		if err.Error() == "invalid_model_test" {
			code = "invalid_model_test"
		}
		gptAccountError(c, status, code)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": view})
}

func GetGPTAccountStatusCheck(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(c.Param("id"))
	if jobID == "" || len(jobID) > 128 {
		gptAccountError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	view, err := service.GetGPTAccountStatusCheck(identity, jobID)
	if err != nil {
		gptAccountError(c, http.StatusNotFound, "status_check_not_found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": view})
}

func CancelGPTAccountStatusCheck(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(c.Param("id"))
	if jobID == "" || len(jobID) > 128 {
		gptAccountError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := service.CancelGPTAccountStatusCheck(identity, jobID); err != nil {
		gptAccountError(c, http.StatusNotFound, "status_check_not_found")
		return
	}
	view, err := service.GetGPTAccountStatusCheck(identity, jobID)
	if err != nil {
		gptAccountError(c, http.StatusNotFound, "status_check_not_found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": view})
}
