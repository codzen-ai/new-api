package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The MuleRouter group registers a wildcard tail under a static /vendors
// segment while the relay router already registers a root-level `/:mode/mj`
// parameter route. Gin panics on conflicting route trees, so registering both
// together is the contract worth pinning.
func TestMuleRouterRoutesCoexistWithRelayRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	require.NotPanics(t, func() {
		SetRelayRouter(engine)
		SetVideoRouter(engine)
		SetMuleRouterRouter(engine)
	})

	routes := make(map[string]struct{}, len(engine.Routes()))
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	_, hasSubmit := routes[http.MethodPost+" /vendors/:vendor/v1/*rest"]
	_, hasFetch := routes[http.MethodGet+" /vendors/:vendor/v1/*rest"]
	assert.True(t, hasSubmit)
	assert.True(t, hasFetch)
}
