package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MuleRouter's native /vendors paths are not served: they answer exactly as
// any other unregistered path does, whatever the method.
func TestVendorsPathsAnswerLikeUnregisteredPaths(t *testing.T) {
	setupRelayRouterTestDB(t)
	t.Setenv("FRONTEND_BASE_URL", "")
	engine := gin.New()
	SetRouter(engine, WebAssets{IndexPage: []byte("<html>index</html>")})

	serve := func(method, path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, strings.NewReader(`{"prompt":"a cat"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer sk-anything")
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			vendors := serve(method, "/vendors/carrothub/v1/z-image-spicy/generation")
			unregistered := serve(method, "/not-registered/carrothub/v1/z-image-spicy/generation")

			require.Equal(t, http.StatusOK, unregistered.Code)
			assert.Equal(t, unregistered.Code, vendors.Code)
			assert.Equal(t, unregistered.Header().Get("Content-Type"), vendors.Header().Get("Content-Type"))
			assert.Equal(t, unregistered.Body.String(), vendors.Body.String())
		})
	}
}
