package plugins_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel/task/mulerouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Go MuleRouter channel on staging (channel 87), copied verbatim. The
// plugin must charge exactly what this configuration charges.
const (
	mulerouterStagingRoutes = `{"routes":[
		{"vendor":"carrothub","model":"qwen-image-edit-spicy"},
		{"vendor":"carrothub","model":"z-image-spicy","billing_vars":[
			{"name":"prompt_extend","kind":"enum","values":{"true":1.076923,"false":1},"default":"true"},
			{"name":"width","kind":"int","min":256,"max":1536,"default":"1024"},
			{"name":"height","kind":"int","min":256,"max":1536,"default":"1536"}]},
		{"vendor":"carrothub","model":"wan2.2-i2v-spicy","billing_vars":[
			{"name":"seconds","source":"duration","kind":"int","enum":[5,8],"default":"5","divisor":1},
			{"name":"resolution","kind":"enum","values":{"480p":1,"720p":2},"default":"480p"}]}]}`
	mulerouterStagingModelMapping = `{
		"z-image-spicy": "carrothub/z-image-spicy/generation",
		"qwen-image-edit-spicy": "carrothub/qwen-image-edit-spicy/generation",
		"wan2.2-i2v-spicy": "carrothub/wan2.2-i2v-spicy/generation"}`
	mulerouterBaseURL = "https://api.mulerouter.ai"
)

// mulerouterSubmission is what a channel does with one client submission:
// the upstream request it would send and the billing multipliers it would
// apply, or the reason it refuses the request before charging.
type mulerouterSubmission struct {
	URL       string
	Body      map[string]any
	Ratios    map[string]float64
	Rejection string
}

func newMulerouterPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("mulerouter")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "mulerouter"})
	require.NoError(t, err)
	return plugin
}

// mulerouterSubmitContext is a client request on the unified task route, with
// the channel model mapping applied the way the task relay applies it.
func mulerouterSubmitContext(t *testing.T, publicModel, body string, info *relaycommon.RelayInfo) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("model_mapping", mulerouterStagingModelMapping)
	info.OriginModelName = publicModel
	info.UpstreamModelName = publicModel
	require.NoError(t, helper.ModelMappedHelper(c, info, nil))
	return c
}

func readMulerouterBody(t *testing.T, reader io.Reader) map[string]any {
	t.Helper()
	encoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, common.Unmarshal(encoded, &body))
	return body
}

func submitThroughGoMulerouter(t *testing.T, publicModel, body string) mulerouterSubmission {
	t.Helper()
	var config kitdto.MuleRouterConfig
	require.NoError(t, common.UnmarshalJsonStr(mulerouterStagingRoutes, &config))
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k", ChannelOtherSettings: kitdto.ChannelOtherSettings{MuleRouter: &config}},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	c := mulerouterSubmitContext(t, publicModel, body, info)
	adaptor := &mulerouter.TaskAdaptor{}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		return mulerouterSubmission{Rejection: taskErr.Message}
	}
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	reader, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	return mulerouterSubmission{URL: url, Body: readMulerouterBody(t, reader), Ratios: adaptor.EstimateBilling(c, info)}
}

func submitThroughMulerouterPlugin(t *testing.T, plugin *jsplugin.LoadedPlugin, publicModel, body string) mulerouterSubmission {
	t.Helper()
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelType: 61, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	c := mulerouterSubmitContext(t, publicModel, body, info)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		return mulerouterSubmission{Rejection: taskErr.Message}
	}
	ratios, err := adaptor.EstimateBillingValidated(c, info)
	if err != nil {
		return mulerouterSubmission{Rejection: err.Error()}
	}
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	reader, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	return mulerouterSubmission{URL: url, Body: readMulerouterBody(t, reader), Ratios: ratios}
}

// submitThroughMulerouterVideosAPI is a client request on POST /v1/videos: the
// host has pinned the plugin's openai_video binding for the public model, and
// the channel model mapping names the MuleRouter endpoint.
func submitThroughMulerouterVideosAPI(t *testing.T, plugin *jsplugin.LoadedPlugin, publicModel, body string) mulerouterSubmission {
	t.Helper()
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelType: 61, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	c := mulerouterSubmitContext(t, publicModel, body, info)
	c.Request.URL.Path = "/v1/videos"
	var value any
	require.NoError(t, common.UnmarshalJsonStr(body, &value))
	protocolRequest := jsplugin.ProtocolRequestContext{
		RouteRequestContext: jsplugin.RouteRequestContext{Body: map[string]any{"kind": "json", "value": value}},
		Protocol:            "openai_video", Operation: "create", Model: publicModel, UpstreamModel: info.UpstreamModelName,
	}
	// Channel selection decodes first and answers a refusal with the
	// decoder's own message.
	if _, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, protocolRequest.JSValueFor(plugin.Meta)); err != nil {
		return mulerouterSubmission{Rejection: err.Error()}
	}
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{Plugin: plugin, Protocol: "openai_video", Model: publicModel, MappedModel: info.UpstreamModelName})
	c.Set(jsplugin.ContextKeyProtocolRequest, protocolRequest)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		return mulerouterSubmission{Rejection: taskErr.Message}
	}
	ratios, err := adaptor.EstimateBillingValidated(c, info)
	if err != nil {
		return mulerouterSubmission{Rejection: err.Error()}
	}
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	reader, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	return mulerouterSubmission{URL: url, Body: readMulerouterBody(t, reader), Ratios: ratios}
}

func TestMulerouterPluginIsABuiltInPluginOnTaskPluginChannelsOnly(t *testing.T) {
	plugin, found := jsplugin.DefaultRegistry.Generation().Get("mulerouter")
	require.True(t, found)
	assert.Empty(t, plugin.Meta.ChannelTypes, "type-64 channels and their tasks stay with the Go adaptor")
	assert.Empty(t, plugin.Meta.Routes)
	// Only the video endpoint is served on the OpenAI video API; the image
	// endpoints answer only on the unified task route.
	require.Len(t, plugin.Meta.Protocols, 1)
	assert.Equal(t, "openai_video", plugin.Meta.Protocols[0].Name)
	assert.Equal(t, []string{"carrothub/wan2.2-i2v-spicy/generation"}, plugin.Meta.Protocols[0].Models)
	assert.Equal(t, mulerouterBaseURL, plugin.Meta.BaseURL)
	assert.ElementsMatch(t, []string{"carrothub/z-image-spicy/generation", "carrothub/qwen-image-edit-spicy/generation", "carrothub/wan2.2-i2v-spicy/generation"}, plugin.Meta.Models)
}

// Every case runs through the Go MuleRouter adaptor (billing_vars and request
// body construction) and through the plugin, and both must agree on the
// upstream request and the billing multipliers, or both refuse it. Later
// MuleRouter endpoints add their cases to this table.
func TestMulerouterPluginMatchesGoAdaptor(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	const (
		zImage   = mulerouterBaseURL + "/vendors/carrothub/v1/z-image-spicy/generation"
		qwenEdit = mulerouterBaseURL + "/vendors/carrothub/v1/qwen-image-edit-spicy/generation"
		wanVideo = mulerouterBaseURL + "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation"
	)

	// The host hands the plugin its own re-encoding of the request, so a few
	// inputs cannot reach the plugin as the client wrote them. These are known
	// host-caused differences, recorded per case:
	// - pluginUpload is the body the plugin sends instead of Go's.
	// - pluginRatios, with pluginUpload, is what the plugin bills for a
	//   request Go refuses: the host drops a top-level duration it cannot
	//   decode into an integer (5.5, "eight", 0), so the plugin sees no
	//   duration, bills the 5-second default and sends none upstream.
	// - pluginReject is the refusal the plugin answers with instead of Go's.
	cases := []struct {
		name         string
		model        string
		body         string
		url          string
		upload       map[string]any
		ratios       map[string]float64
		reject       string
		pluginUpload map[string]any
		pluginRatios map[string]float64
		pluginReject string
	}{
		{
			name:   "z-image defaults bill prompt rewriting",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat"}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat"},
			ratios: map[string]float64{"prompt_extend": 1.076923},
		},
		{
			name:   "z-image prompt_extend false bills the base price",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"prompt_extend":false}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "prompt_extend": false},
			ratios: map[string]float64{"prompt_extend": 1},
		},
		{
			name:   "z-image prompt_extend true",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"prompt_extend":true}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "prompt_extend": true},
			ratios: map[string]float64{"prompt_extend": 1.076923},
		},
		{
			name:   "z-image prompt_extend as a string",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"prompt_extend":"false"}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "prompt_extend": "false"},
			ratios: map[string]float64{"prompt_extend": 1},
		},
		{
			name:   "z-image width and height are bounded but not priced",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":1536,"height":256,"seed":0}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "width": float64(1536), "height": float64(256), "seed": float64(0)},
			ratios: map[string]float64{"prompt_extend": 1.076923},
		},
		{
			name:   "z-image null width bills its default",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":null}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "width": nil},
			ratios: map[string]float64{"prompt_extend": 1.076923},
		},
		{
			name:   "metadata wins over the top level and routing fields are dropped",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"top","metadata":{"prompt":"meta","model":"other","negative_prompt":"blur"}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "meta", "negative_prompt": "blur"},
			ratios: map[string]float64{"prompt_extend": 1.076923},
		},
		{
			name:   "z-image width at its minimum and height at its maximum",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":256,"height":1536}}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "width": float64(256), "height": float64(1536)},
			ratios: map[string]float64{"prompt_extend": 1.076923},
		},
		{
			name:   "z-image width just above the bound",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":1537}}`,
			reject: "width must be <= 1536",
		},
		{
			name:   "z-image width just below the bound",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":255}}`,
			reject: "width must be >= 256",
		},
		{
			name:   "z-image height just above the bound",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"height":1537}}`,
			reject: "height must be <= 1536",
		},
		{
			name:   "z-image height just below the bound",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"height":255}}`,
			reject: "height must be >= 256",
		},
		{
			name:   "z-image fractional width",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":512.5}}`,
			reject: "width must be an integer, got 512.5",
		},
		{
			name:   "z-image width that is not a number",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":"wide"}}`,
			reject: `invalid value for width: "wide" is not a number`,
		},
		{
			name:   "z-image unknown prompt_extend",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"prompt_extend":"maybe"}}`,
			reject: `invalid value "maybe" for prompt_extend, allowed: false, true`,
		},
		{
			name:   "undeclared cost field hidden in metadata",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"n":2}}`,
			reject: "parameter(s) n affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:   "several undeclared cost fields are named in order",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"num_images":2,"fps":24}}`,
			reject: "parameter(s) fps, num_images affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:   "undeclared cost field among the unified fields",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","size":"1024x1024","duration":5}`,
			reject: "parameter(s) duration, size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:   "qwen-image-edit sends the input image as the client wrote it and has no multipliers",
			model:  "qwen-image-edit-spicy",
			body:   `{"model":"qwen-image-edit-spicy","prompt":"snowy background","image":"https://cdn/in.png","metadata":{"seed":7}}`,
			url:    qwenEdit,
			upload: map[string]any{"prompt": "snowy background", "image": "https://cdn/in.png", "seed": float64(7)},
			ratios: map[string]float64{},
		},
		{
			name:   "qwen-image-edit forwards an images list the client sent",
			model:  "qwen-image-edit-spicy",
			body:   `{"model":"qwen-image-edit-spicy","prompt":"snowy background","images":["https://cdn/a.png","https://cdn/b.png"]}`,
			url:    qwenEdit,
			upload: map[string]any{"prompt": "snowy background", "images": []any{"https://cdn/a.png", "https://cdn/b.png"}},
			ratios: map[string]float64{},
		},
		{
			name:   "qwen-image-edit takes the input image from metadata",
			model:  "qwen-image-edit-spicy",
			body:   `{"model":"qwen-image-edit-spicy","prompt":"snowy background","image":"https://cdn/top.png","metadata":{"image":"https://cdn/meta.png"}}`,
			url:    qwenEdit,
			upload: map[string]any{"prompt": "snowy background", "image": "https://cdn/meta.png"},
			ratios: map[string]float64{},
		},
		{
			name:   "qwen-image-edit refuses a cost field it does not price",
			model:  "qwen-image-edit-spicy",
			body:   `{"model":"qwen-image-edit-spicy","prompt":"snowy background","image":"https://cdn/in.png","metadata":{"width":1024,"n":2}}`,
			reject: "parameter(s) n, width affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:   "qwen-image-edit refuses a unified cost field",
			model:  "qwen-image-edit-spicy",
			body:   `{"model":"qwen-image-edit-spicy","prompt":"snowy background","image":"https://cdn/in.png","size":"1024x1024"}`,
			reject: "parameter(s) size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:   "wan2.2 bills 5 seconds at 480p",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"push in","image":"https://cdn/in.png","duration":5,"metadata":{"resolution":"480p"}}`,
			url:    wanVideo,
			upload: map[string]any{"prompt": "push in", "image": "https://cdn/in.png", "duration": float64(5), "resolution": "480p"},
			ratios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:   "wan2.2 bills 8 seconds at 720p",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":8,"metadata":{"resolution":"720p"}}`,
			url:    wanVideo,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8), "resolution": "720p"},
			ratios: map[string]float64{"seconds": 8, "resolution": 2},
		},
		{
			name:   "wan2.2 omitted resolution bills 480p",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":8}`,
			url:    wanVideo,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
			ratios: map[string]float64{"seconds": 8, "resolution": 1},
		},
		{
			name:   "wan2.2 duration in metadata wins over the top level",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5,"metadata":{"duration":8}}`,
			url:    wanVideo,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
			ratios: map[string]float64{"seconds": 8, "resolution": 1},
		},
		{
			// The host decodes a string duration into a number; the price is the same.
			name:         "wan2.2 duration written as a string",
			model:        "wan2.2-i2v-spicy",
			body:         `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":"8"}`,
			url:          wanVideo,
			upload:       map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": "8"},
			ratios:       map[string]float64{"seconds": 8, "resolution": 1},
			pluginUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
		},
		{
			name:   "wan2.2 omitted duration bills 5 seconds",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png"}`,
			url:    wanVideo,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			ratios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			// The host's request bounds refuse a null duration at any depth
			// before the plugin runs; Go bills it as the default.
			name:         "wan2.2 null duration in metadata",
			model:        "wan2.2-i2v-spicy",
			body:         `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"duration":null}}`,
			url:          wanVideo,
			upload:       map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": nil},
			ratios:       map[string]float64{"seconds": 5, "resolution": 1},
			pluginReject: "plugin usage value must be a number",
		},
		{
			name:         "wan2.2 fractional top-level duration",
			model:        "wan2.2-i2v-spicy",
			body:         `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5.5}`,
			url:          wanVideo,
			reject:       "duration must be an integer, got 5.5",
			pluginUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			pluginRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:         "wan2.2 top-level duration that is not a number",
			model:        "wan2.2-i2v-spicy",
			body:         `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":"eight"}`,
			url:          wanVideo,
			reject:       `invalid value for duration: "eight" is not a number`,
			pluginUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			pluginRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:         "wan2.2 top-level duration 0",
			model:        "wan2.2-i2v-spicy",
			body:         `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":0}`,
			url:          wanVideo,
			reject:       "invalid value 0 for duration, allowed: 5, 8",
			pluginUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			pluginRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:   "wan2.2 fractional duration in metadata",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"duration":5.5}}`,
			reject: "duration must be an integer, got 5.5",
		},
		{
			name:   "wan2.2 duration outside its values",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":6}`,
			reject: "invalid value 6 for duration, allowed: 5, 8",
		},
		{
			name:   "wan2.2 duration just below its values",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":4}`,
			reject: "invalid value 4 for duration, allowed: 5, 8",
		},
		{
			name:   "wan2.2 duration just above its values",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":9}`,
			reject: "invalid value 9 for duration, allowed: 5, 8",
		},
		{
			name:   "wan2.2 unknown resolution",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5,"metadata":{"resolution":"1080p"}}`,
			reject: `invalid value "1080p" for resolution, allowed: 480p, 720p`,
		},
		{
			name:   "wan2.2 refuses seconds, which it reads from duration",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5,"metadata":{"seconds":8,"fps":24}}`,
			reject: "parameter(s) fps, seconds affect the price of this model and are not enabled on this endpoint; remove them",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goSubmission := submitThroughGoMulerouter(t, tc.model, tc.body)
			pluginSubmission := submitThroughMulerouterPlugin(t, plugin, tc.model, tc.body)
			if tc.pluginRatios != nil {
				assert.Equal(t, tc.reject, goSubmission.Rejection)
				assert.Equal(t, mulerouterSubmission{URL: tc.url, Body: tc.pluginUpload, Ratios: tc.pluginRatios}, pluginSubmission)
				return
			}
			pluginReject := tc.pluginReject
			if pluginReject == "" {
				pluginReject = tc.reject
			}
			if pluginReject != "" {
				assert.Contains(t, pluginSubmission.Rejection, pluginReject)
				assert.NotContains(t, pluginSubmission.Rejection, "carrothub")
			}
			if tc.reject != "" {
				assert.Equal(t, tc.reject, goSubmission.Rejection)
				return
			}
			require.Empty(t, goSubmission.Rejection)
			assert.Equal(t, mulerouterSubmission{URL: tc.url, Body: tc.upload, Ratios: tc.ratios}, goSubmission)
			if pluginReject != "" {
				return
			}
			require.Empty(t, pluginSubmission.Rejection)
			expected := goSubmission
			if tc.pluginUpload != nil {
				expected.Body = tc.pluginUpload
			}
			assert.Equal(t, expected, pluginSubmission)
		})
	}
}

// POST /v1/videos takes the unified task body, so a video submission must send
// the same upstream request and bill the same multipliers there as on
// /v1/video/generations, through the plugin and through the Go adaptor, or be
// refused by all three. The cases cover each default and every enum value.
func TestMulerouterPluginVideosAPIBillsLikeTheTaskRoute(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	const wanVideo = mulerouterBaseURL + "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation"
	cases := []struct {
		name   string
		body   string
		upload map[string]any
		ratios map[string]float64
		reject string
	}{
		{
			name:   "defaults bill 5 seconds at 480p",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png"}`,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			ratios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:   "duration 5 at 480p",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5,"metadata":{"resolution":"480p"}}`,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(5), "resolution": "480p"},
			ratios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:   "duration 8 with the default resolution",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":8}`,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
			ratios: map[string]float64{"seconds": 8, "resolution": 1},
		},
		{
			name:   "720p with the default duration",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"resolution":"720p","seed":7}}`,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "resolution": "720p", "seed": float64(7)},
			ratios: map[string]float64{"seconds": 5, "resolution": 2},
		},
		{
			name:   "duration 8 at 720p from metadata",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"duration":8,"resolution":"720p"}}`,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8), "resolution": "720p"},
			ratios: map[string]float64{"seconds": 8, "resolution": 2},
		},
		{
			name:   "duration outside its values",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":6}`,
			reject: "invalid value 6 for duration, allowed: 5, 8",
		},
		{
			name:   "unknown resolution",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"resolution":"1080p"}}`,
			reject: `invalid value "1080p" for resolution, allowed: 480p, 720p`,
		},
		{
			name:   "the OpenAI seconds and size fields are not priced",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","seconds":"8","size":"1280x720"}`,
			reject: "parameter(s) seconds, size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:   "missing prompt",
			body:   `{"model":"wan2.2-i2v-spicy","image":"https://cdn/in.png"}`,
			reject: "prompt is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			videos := submitThroughMulerouterVideosAPI(t, plugin, "wan2.2-i2v-spicy", tc.body)
			taskRoute := submitThroughMulerouterPlugin(t, plugin, "wan2.2-i2v-spicy", tc.body)
			goSubmission := submitThroughGoMulerouter(t, "wan2.2-i2v-spicy", tc.body)
			if tc.reject != "" {
				assert.Equal(t, tc.reject, goSubmission.Rejection)
				for _, submission := range []mulerouterSubmission{videos, taskRoute} {
					assert.Contains(t, submission.Rejection, tc.reject)
					assert.NotContains(t, submission.Rejection, "carrothub")
				}
				return
			}
			expected := mulerouterSubmission{URL: wanVideo, Body: tc.upload, Ratios: tc.ratios}
			assert.Equal(t, expected, goSubmission)
			assert.Equal(t, expected, taskRoute)
			assert.Equal(t, expected, videos)
		})
	}
}

func TestMulerouterPluginRejectsEndpointsMissingFromItsTable(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	for _, tc := range []struct {
		name  string
		model string
	}{
		{"model without a channel mapping", "carrothub/unknown-spicy/generation"},
		{"public name the channel does not map", "z-image-spicy-v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submission := submitThroughMulerouterPlugin(t, plugin, tc.model, `{"model":"`+tc.model+`","prompt":"a cat"}`)
			require.NotEmpty(t, submission.Rejection)
			assert.Contains(t, submission.Rejection, "not available")
			assert.NotContains(t, submission.Rejection, "carrothub")
			assert.NotContains(t, submission.Rejection, "/generation")
		})
	}
}

// The upstream submit and poll responses are read the same way by both
// channels: the task id, the gateway status, the failure reason and the
// result URL.
func TestMulerouterPluginReadsTasksLikeGoAdaptor(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	goAdaptor := &mulerouter.TaskAdaptor{}
	pluginAdaptor := taskplugin.New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: 61, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k", UpstreamModelName: "carrothub/z-image-spicy/generation"},
		OriginModelName: "z-image-spicy",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	goAdaptor.Init(info)
	pluginAdaptor.Init(info)

	t.Run("submit response", func(t *testing.T) {
		submit := func(adaptor relaychannel.TaskAdaptor, body string) (string, *taskdto.TaskError) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
			resp := &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			parsed, taskErr := adaptor.ParseResponse(c, resp, info)
			if taskErr != nil {
				return "", taskErr
			}
			return parsed.UpstreamTaskID, nil
		}
		accepted := `{"task_info":{"id":"up-1","status":"pending","created_at":"2026-10-20T00:00:00Z"}}`
		goID, goErr := submit(goAdaptor, accepted)
		require.Nil(t, goErr)
		pluginID, pluginErr := submit(pluginAdaptor, accepted)
		require.Nil(t, pluginErr)
		assert.Equal(t, "up-1", goID)
		assert.Equal(t, goID, pluginID)

		for _, body := range []string{`{"task_info":{"status":"pending"}}`, `{"task_info":{"id":"up-1","status":"failed","error":{"title":"bad_prompt","detail":"blocked"}}}`} {
			_, goErr = submit(goAdaptor, body)
			require.NotNil(t, goErr, body)
			_, pluginErr = submit(pluginAdaptor, body)
			require.NotNil(t, pluginErr, body)
		}
	})

	t.Run("query request", func(t *testing.T) {
		for _, tc := range []struct{ publicModel, upstreamModel, path string }{
			{"z-image-spicy", "carrothub/z-image-spicy/generation", "/vendors/carrothub/v1/z-image-spicy/generation/up-1"},
			{"wan2.2-i2v-spicy", "carrothub/wan2.2-i2v-spicy/generation", "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation/up-1"},
		} {
			requests := make([]*http.Request, 0, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"task_info":{"id":"up-1","status":"processing"}}`)
			}))
			task := &model.Task{TaskID: "task_public", Properties: model.Properties{OriginModelName: tc.publicModel, UpstreamModelName: tc.upstreamModel}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "up-1"}}
			for _, adaptor := range []relaychannel.TaskAdaptor{goAdaptor, pluginAdaptor} {
				resp, err := adaptor.FetchTask(server.URL, "k", task, "")
				require.NoError(t, err)
				resp.Body.Close()
			}
			server.Close()
			require.Len(t, requests, 2)
			for _, r := range requests {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, tc.path, r.URL.Path)
				assert.Equal(t, "Bearer k", r.Header.Get("Authorization"))
			}
		}
	})

	t.Run("poll results", func(t *testing.T) {
		task := &model.Task{TaskID: "task_public", Properties: model.Properties{OriginModelName: "z-image-spicy", UpstreamModelName: "carrothub/z-image-spicy/generation"}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "up-1"}}
		cases := []struct {
			name   string
			body   string
			status model.TaskStatus
			url    string
			reason string
		}{
			{"pending", `{"task_info":{"id":"up-1","status":"pending"}}`, model.TaskStatusSubmitted, "", ""},
			{"processing", `{"task_info":{"id":"up-1","status":"processing"}}`, model.TaskStatusInProgress, "", ""},
			{"completed image", `{"task_info":{"id":"up-1","status":"completed"},"images":["https://cdn/a.png","https://cdn/b.png"]}`, model.TaskStatusSuccess, "https://cdn/a.png", ""},
			{"completed video", `{"task_info":{"id":"up-1","status":"completed"},"videos":["https://cdn/a.mp4"]}`, model.TaskStatusSuccess, "https://cdn/a.mp4", ""},
			{"completed without artifacts", `{"task_info":{"id":"up-1","status":"completed"}}`, model.TaskStatusSuccess, "", ""},
			{"failed with title and detail", `{"task_info":{"id":"up-1","status":"failed","error":{"code":400,"title":"bad_prompt","detail":"blocked"}}}`, model.TaskStatusFailure, "", "bad_prompt: blocked"},
			{"failed with title only", `{"task_info":{"id":"up-1","status":"failed","error":{"title":"bad_prompt"}}}`, model.TaskStatusFailure, "", "bad_prompt"},
			{"failed with detail only", `{"task_info":{"id":"up-1","status":"failed","error":{"detail":"blocked"}}}`, model.TaskStatusFailure, "", "blocked"},
			{"failed without an error", `{"task_info":{"id":"up-1","status":"failed"}}`, model.TaskStatusFailure, "", "task failed"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				goResult, err := goAdaptor.ParseTaskResult(task, nil, []byte(tc.body))
				require.NoError(t, err)
				pluginResult, err := pluginAdaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, []byte(tc.body))
				require.NoError(t, err)
				for _, result := range []*relaycommon.TaskInfo{goResult, pluginResult} {
					assert.Equal(t, string(tc.status), result.Status)
					assert.Equal(t, tc.url, result.Url)
					assert.Equal(t, tc.reason, result.Reason)
				}
			})
		}

		// The Go adaptor counts an unknown state as a failed poll; the plugin
		// reports UNKNOWN, which the host counts the same way.
		_, err := goAdaptor.ParseTaskResult(task, nil, []byte(`{"task_info":{"id":"up-1","status":"paused"}}`))
		require.Error(t, err)
		pluginResult, err := pluginAdaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, []byte(`{"task_info":{"id":"up-1","status":"paused"}}`))
		require.NoError(t, err)
		assert.Equal(t, "UNKNOWN", pluginResult.Status)
	})
}

// A task the Go adaptor submitted and the startup migration handed to the
// plugin keeps its row as the Go adaptor wrote it, with only the platform
// rewritten. Polling must resolve the plugin from that platform, rebuild the
// query URL from the persisted MuleRouter model name and upstream task id, and
// read the terminal state.
func TestMulerouterPluginFinishesTasksTheGoAdaptorSubmitted(t *testing.T) {
	task := &model.Task{
		TaskID:    "task_go_submitted",
		Platform:  constant.TaskPlatform("mulerouter"),
		ChannelId: 87,
		Action:    "generation",
		Status:    model.TaskStatusInProgress,
		Quota:     1000,
		Properties: model.Properties{
			OriginModelName:   "wan2.2-i2v-spicy",
			UpstreamModelName: "carrothub/wan2.2-i2v-spicy/generation",
		},
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "up-go-1",
			BillingSource:  "wallet",
			BillingContext: &model.TaskBillingContext{
				ModelPrice: 0.2, GroupRatio: 1, OtherRatios: map[string]float64{"seconds": 8, "resolution": 2},
				OriginModelName: "wan2.2-i2v-spicy", PerCallBilling: true,
			},
		},
		Data: []byte(`{"task_info":{"id":"up-go-1","status":"pending","created_at":"2026-10-20T00:00:00Z"}}`),
	}

	adaptor := relay.GetTaskAdaptor(task.Platform)
	require.NotNil(t, adaptor, "the migrated platform resolves to the plugin")
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelId: 87}})

	for _, tc := range []struct {
		name   string
		body   string
		status model.TaskStatus
		url    string
		reason string
	}{
		{"still running", `{"task_info":{"id":"up-go-1","status":"processing"}}`, model.TaskStatusInProgress, "", ""},
		{"completed", `{"task_info":{"id":"up-go-1","status":"completed"},"videos":["https://cdn/out.mp4"]}`, model.TaskStatusSuccess, "https://cdn/out.mp4", ""},
		{"failed", `{"task_info":{"id":"up-go-1","status":"failed","error":{"title":"bad_prompt","detail":"blocked"}}}`, model.TaskStatusFailure, "", "bad_prompt: blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requested *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested = r
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			resp, err := adaptor.FetchTask(server.URL, "sk-channel", task, "")
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.NotNil(t, requested)
			assert.Equal(t, http.MethodGet, requested.Method)
			assert.Equal(t, "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation/up-go-1", requested.URL.Path)
			assert.Equal(t, "Bearer sk-channel", requested.Header.Get("Authorization"))

			result, err := adaptor.ParseTaskResult(task, resp, body)
			require.NoError(t, err)
			assert.Equal(t, string(tc.status), result.Status)
			assert.Equal(t, tc.url, result.Url)
			assert.Equal(t, tc.reason, result.Reason)
		})
	}
}

// GET /v1/videos/{task_id} renders a MuleRouter task as an OpenAI video object.
// Tasks the Go adaptor submitted and the startup migration handed to the
// plugin, and tasks the plugin submitted itself, have the same row shape: the
// persisted data is the latest upstream poll body. The plugin must render each
// state as the Go adaptor's ConvertToOpenAIVideo does. The one difference is
// host-owned: the host sends completed_at only for a completed task, where the
// Go adaptor also stamps an unfinished task with its last update time.
func TestMulerouterPluginRendersOpenAIVideoLikeGoAdaptor(t *testing.T) {
	var goConverter relaychannel.OpenAIVideoConverter = &mulerouter.TaskAdaptor{}
	pluginConverter, ok := relay.GetTaskAdaptor(constant.TaskPlatform("mulerouter")).(relaychannel.OpenAIVideoConverter)
	require.True(t, ok)

	videoTask := func(channelID int, status model.TaskStatus, progress, failReason, data string) *model.Task {
		task := &model.Task{
			TaskID:      "task_public",
			Platform:    constant.TaskPlatform("mulerouter"),
			ChannelId:   channelID,
			Action:      "generation",
			Status:      status,
			Progress:    progress,
			FailReason:  failReason,
			CreatedAt:   1786370025,
			UpdatedAt:   1786370124,
			Properties:  model.Properties{OriginModelName: "wan2.2-i2v-spicy", UpstreamModelName: "carrothub/wan2.2-i2v-spicy/generation"},
			PrivateData: model.TaskPrivateData{UpstreamTaskID: "up-1"},
			Data:        []byte(data),
		}
		if status == model.TaskStatusSuccess {
			task.FinishTime = task.UpdatedAt
		}
		return task
	}

	cases := []struct {
		name          string
		status        model.TaskStatus
		progress      string
		failReason    string
		data          string
		want          string
		goCompletedAt bool
	}{
		{
			name:          "queued",
			status:        model.TaskStatusSubmitted,
			data:          `{"task_info":{"id":"up-1","status":"pending","created_at":"2026-10-20T00:00:00Z"}}`,
			want:          `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"queued","progress":0,"created_at":1786370025}`,
			goCompletedAt: true,
		},
		{
			name:          "processing",
			status:        model.TaskStatusInProgress,
			progress:      "30%",
			data:          `{"task_info":{"id":"up-1","status":"processing"}}`,
			want:          `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"in_progress","progress":30,"created_at":1786370025}`,
			goCompletedAt: true,
		},
		{
			name:     "completed carries the result URL",
			status:   model.TaskStatusSuccess,
			progress: "100%",
			data:     `{"task_info":{"id":"up-1","status":"completed"},"videos":["https://cdn/out.mp4","https://cdn/alt.mp4"]}`,
			want:     `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"completed","progress":100,"created_at":1786370025,"completed_at":1786370124,"metadata":{"url":"https://cdn/out.mp4"}}`,
		},
		{
			name:          "failed carries the failure reason",
			status:        model.TaskStatusFailure,
			progress:      "100%",
			failReason:    "bad_prompt: blocked",
			data:          `{"task_info":{"id":"up-1","status":"failed","error":{"title":"bad_prompt","detail":"blocked"}}}`,
			want:          `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"failed","progress":100,"created_at":1786370025,"error":{"message":"bad_prompt: blocked","code":"task_failed"}}`,
			goCompletedAt: true,
		},
	}
	for _, origin := range []struct {
		name      string
		channelID int
	}{{"submitted by the Go adaptor", 87}, {"submitted by the plugin", 61}} {
		for _, tc := range cases {
			t.Run(origin.name+"/"+tc.name, func(t *testing.T) {
				task := videoTask(origin.channelID, tc.status, tc.progress, tc.failReason, tc.data)
				pluginBody, err := pluginConverter.ConvertToOpenAIVideo(task)
				require.NoError(t, err)
				assert.JSONEq(t, tc.want, string(pluginBody))

				goBody, err := goConverter.ConvertToOpenAIVideo(task)
				require.NoError(t, err)
				var goVideo map[string]any
				require.NoError(t, common.Unmarshal(goBody, &goVideo))
				if tc.goCompletedAt {
					assert.Equal(t, float64(task.UpdatedAt), goVideo["completed_at"])
					delete(goVideo, "completed_at")
				}
				goJSON, err := common.Marshal(goVideo)
				require.NoError(t, err)
				assert.JSONEq(t, tc.want, string(goJSON))
			})
		}
	}

	// GET /v1/videos/{task_id}/content streams the result URL, as the Go
	// channel's tasks do through their stored result URL.
	t.Run("content is the first result video", func(t *testing.T) {
		adaptor := relay.GetTaskAdaptor(constant.TaskPlatform("mulerouter"))
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k"}})
		provider, ok := adaptor.(relaychannel.TaskContentRequestProvider)
		require.True(t, ok)
		artifacts, ok := adaptor.(relaychannel.TaskArtifactProvider)
		require.True(t, ok)

		done := videoTask(61, model.TaskStatusSuccess, "100%", "", `{"task_info":{"id":"up-1","status":"completed"},"videos":["https://cdn/out.mp4","https://cdn/alt.mp4"]}`)
		listed, err := artifacts.ListArtifacts(done)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, "video", listed[0].Key)
		assert.Equal(t, "video", listed[0].Type)
		descriptor, err := provider.BuildContentRequest(t.Context(), done, "video", relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/out.mp4", descriptor.URL)
		assert.True(t, descriptor.Credentialless)

		running := videoTask(61, model.TaskStatusInProgress, "30%", "", `{"task_info":{"id":"up-1","status":"processing"}}`)
		listed, err = artifacts.ListArtifacts(running)
		require.NoError(t, err)
		assert.Empty(t, listed)
	})

	t.Run("an image task is not a video", func(t *testing.T) {
		task := videoTask(61, model.TaskStatusSuccess, "100%", "", `{"task_info":{"id":"up-1","status":"completed"},"images":["https://cdn/a.png"]}`)
		task.Properties = model.Properties{OriginModelName: "z-image-spicy", UpstreamModelName: "carrothub/z-image-spicy/generation"}
		_, err := goConverter.ConvertToOpenAIVideo(task)
		require.Error(t, err)
		_, err = pluginConverter.ConvertToOpenAIVideo(task)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "carrothub")
	})
}
