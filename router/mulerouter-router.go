package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

// SetMuleRouterRouter registers MuleRouter's vendor-compatible routes:
//
//	POST /vendors/{vendor}/v1/{model}/{action}
//	GET  /vendors/{vendor}/v1/{model}/{action}/{task_id}
//
// The action segment varies in length across vendors (generation,
// text-to-video, video-diffusion), so the tail is a wildcard the middleware
// splits by segment count rather than a fixed pattern.
func SetMuleRouterRouter(router *gin.Engine) {
	muleRouterGroup := router.Group("/vendors")
	muleRouterGroup.Use(middleware.RouteTag("relay"))
	muleRouterGroup.Use(middleware.MuleRouterRequestConvert(), middleware.TokenAuth(), middleware.Distribute())
	{
		muleRouterGroup.POST("/:vendor/v1/*rest", controller.RelayTask)
		muleRouterGroup.GET("/:vendor/v1/*rest", controller.RelayTaskFetch)
	}
}
