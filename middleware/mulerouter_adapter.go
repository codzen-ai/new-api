package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/task/mulerouter"

	"github.com/gin-gonic/gin"
)

// MuleRouterRequestConvert translates MuleRouter's vendor-native routes
//
//	POST   /vendors/{vendor}/v1/{model}/{action}
//	GET    /vendors/{vendor}/v1/{model}/{action}/{task_id}
//
// into new-api's unified task request, so existing MuleRouter clients only have
// to swap base URL and key. The vendor body is preserved verbatim under
// metadata and forwarded upstream unchanged.
func MuleRouterRequestConvert() func(c *gin.Context) {
	return func(c *gin.Context) {
		parsed, ok := mulerouter.ParsePath(c.Param("vendor"), c.Param("rest"))
		if !ok {
			abortWithMuleRouterError(c, http.StatusNotFound, "unsupported MuleRouter path")
			return
		}
		c.Set(mulerouter.ContextKeyNativeRoute, true)

		if c.Request.Method == http.MethodGet {
			if parsed.TaskID == "" {
				abortWithMuleRouterError(c, http.StatusNotFound, "missing task id")
				return
			}
			c.Set("task_id", parsed.TaskID)
			c.Next()
			return
		}
		if parsed.TaskID != "" {
			abortWithMuleRouterError(c, http.StatusNotFound, "unsupported MuleRouter path")
			return
		}

		var originalReq map[string]any
		if err := common.UnmarshalBodyReusable(c, &originalReq); err != nil {
			abortWithMuleRouterError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		prompt, _ := originalReq["prompt"].(string)
		image, _ := originalReq["image"].(string)
		unifiedReq := map[string]any{
			"model":    parsed.ModelName(),
			"prompt":   prompt,
			"image":    image,
			"metadata": originalReq,
		}

		jsonData, err := common.Marshal(unifiedReq)
		if err != nil {
			abortWithMuleRouterError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(jsonData))
		c.Set(common.KeyRequestBody, jsonData)
		c.Next()
	}
}

func abortWithMuleRouterError(c *gin.Context, status int, detail string) {
	c.AbortWithStatusJSON(status, gin.H{"task_info": gin.H{
		"status": "failed",
		"error":  gin.H{"code": status, "title": "invalid_request", "detail": detail},
	}})
}
