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
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/router"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The model mapping of the MuleRouter channel on staging (channel 87).
const (
	mulerouterStagingModelMapping = `{
		"z-image-spicy": "carrothub/z-image-spicy/generation",
		"qwen-image-edit-spicy": "carrothub/qwen-image-edit-spicy/generation",
		"wan2.2-i2v-spicy": "carrothub/wan2.2-i2v-spicy/generation"}`
	mulerouterBaseURL = "https://api.mulerouter.ai"
	// mulerouterPluginKey is the plugin's key, which is also the task platform
	// of the tasks it serves.
	mulerouterPluginKey = "mulerouter"
)

// mulerouterSubmission is what a channel does with one client submission:
// the upstream request it would send and the billing multipliers it would
// apply, or the reason it refuses the request before charging.
type mulerouterSubmission struct {
	URL       string
	Body      map[string]any
	Ratios    map[string]float64
	Action    string
	Rejection string
}

// mulerouterTaskActions is the action a task is filed under, by what its
// public model produces, on either entry.
var mulerouterTaskActions = map[string]string{
	"z-image-spicy":         "text_to_image",
	"qwen-image-edit-spicy": "image_to_image",
	"wan2.2-i2v-spicy":      "image_to_video",
}

func newMulerouterPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source(mulerouterPluginKey)
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: mulerouterPluginKey})
	require.NoError(t, err)
	return plugin
}

// mulerouterSubmitContext is a client submission with the channel model
// mapping applied the way the task relay applies it.
func mulerouterSubmitContext(t *testing.T, publicModel, body string, info *relaycommon.RelayInfo) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/mulerouter", strings.NewReader(body))
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

// mulerouterPluginChannel is a type-61 Task Plugin channel bound to the
// plugin.
func mulerouterPluginChannel() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
}

// submitToMulerouterPlugin runs a prepared client request through the plugin
// task adaptor as the task relay does.
func submitToMulerouterPlugin(t *testing.T, plugin *jsplugin.LoadedPlugin, c *gin.Context, info *relaycommon.RelayInfo) mulerouterSubmission {
	t.Helper()
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
	return mulerouterSubmission{URL: url, Body: readMulerouterBody(t, reader), Ratios: ratios, Action: info.Action}
}

// submitThroughMulerouterPlugin is a client request on POST
// /v1/tasks/mulerouter, which hands the plugin the client's whole JSON body:
// PrepareTaskPluginSubmit stores it as the task request.
func submitThroughMulerouterPlugin(t *testing.T, plugin *jsplugin.LoadedPlugin, publicModel, body string) mulerouterSubmission {
	t.Helper()
	info := mulerouterPluginChannel()
	c := mulerouterSubmitContext(t, publicModel, body, info)
	var request map[string]any
	require.NoError(t, common.UnmarshalBodyReusable(c, &request))
	c.Set("task_request", request)
	return submitToMulerouterPlugin(t, plugin, c, info)
}

// submitThroughMulerouterVideosAPI is a client request on POST /v1/videos: the
// host has pinned the plugin's openai_video binding for the public model, and
// the channel model mapping names the MuleRouter endpoint.
func submitThroughMulerouterVideosAPI(t *testing.T, plugin *jsplugin.LoadedPlugin, publicModel, body string) mulerouterSubmission {
	t.Helper()
	info := mulerouterPluginChannel()
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
	return submitToMulerouterPlugin(t, plugin, c, info)
}

func TestMulerouterPluginIsABuiltInPluginOnTaskPluginChannelsOnly(t *testing.T) {
	plugin, found := jsplugin.DefaultRegistry.Generation().Get(mulerouterPluginKey)
	require.True(t, found)
	assert.Empty(t, plugin.Meta.ChannelTypes, "the plugin serves only Task Plugin channels bound to it")
	assert.Empty(t, plugin.Meta.Routes)
	// Only the video endpoint is served on the OpenAI video API; the image
	// endpoints answer only on POST /v1/tasks/mulerouter.
	require.Len(t, plugin.Meta.Protocols, 1)
	assert.Equal(t, "openai_video", plugin.Meta.Protocols[0].Name)
	assert.Equal(t, []string{"carrothub/wan2.2-i2v-spicy/generation"}, plugin.Meta.Protocols[0].Models)
	assert.Equal(t, mulerouterBaseURL, plugin.Meta.BaseURL)
	assert.ElementsMatch(t, []string{"carrothub/z-image-spicy/generation", "carrothub/qwen-image-edit-spicy/generation", "carrothub/wan2.2-i2v-spicy/generation"}, plugin.Meta.Models)
}

// A client submission on POST /v1/tasks/mulerouter sends this upstream request
// and bills these multipliers, or is refused before it is charged. The table
// covers every MuleRouter endpoint the plugin declares. Its expectations were
// generated by the Go MuleRouter adaptor this plugin replaced, running the
// staging channel's routes, so each row pins the charge the Go channel made.
// Later MuleRouter endpoints add their cases to this table.
func TestMulerouterPluginSubmissions(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	const (
		zImage   = mulerouterBaseURL + "/vendors/carrothub/v1/z-image-spicy/generation"
		qwenEdit = mulerouterBaseURL + "/vendors/carrothub/v1/qwen-image-edit-spicy/generation"
		wanVideo = mulerouterBaseURL + "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation"
	)

	cases := []struct {
		name   string
		model  string
		body   string
		url    string
		upload map[string]any
		ratios map[string]float64
		reject string
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
			name:   "z-image without a prompt",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","metadata":{"width":1024}}`,
			reject: "prompt is required",
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
			name:   "z-image top-level explicit zero seed and prompt_extend false reach upstream",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","seed":0,"prompt_extend":false}`,
			url:    zImage,
			upload: map[string]any{"prompt": "a cat", "seed": float64(0), "prompt_extend": false},
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
			name:   "undeclared top-level cost fields",
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
			name:   "qwen-image-edit refuses a top-level cost field",
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
			name:   "wan2.2 duration written as a string",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":"8"}`,
			url:    wanVideo,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": "8"},
			ratios: map[string]float64{"seconds": 8, "resolution": 1},
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
			// before the plugin runs. The Go adaptor billed it as the default
			// 5 seconds at 480p.
			name:   "wan2.2 null duration in metadata",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"duration":null}}`,
			reject: "plugin usage value must be a number",
		},
		{
			name:   "wan2.2 fractional top-level duration",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5.5}`,
			reject: "duration must be an integer, got 5.5",
		},
		{
			// The host checks every duration in the request before the plugin
			// runs, so it refuses this instead of the endpoint. The Go adaptor
			// refused it too, saying the duration is not a number.
			name:   "wan2.2 top-level duration that is not a number",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":"eight"}`,
			reject: "plugin usage value must be a number",
		},
		{
			name:   "wan2.2 top-level duration 0",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":0}`,
			reject: "invalid value 0 for duration, allowed: 5, 8",
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
			// The host bounds every duration in the request before the plugin
			// runs, so it refuses these instead of the endpoint. The Go adaptor
			// refused them too, saying the seconds or the duration were out of
			// range.
			name:   "wan2.2 negative top-level duration",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":-5}`,
			reject: "plugin usage value must be a finite non-negative number",
		},
		{
			name:   "wan2.2 negative duration in metadata",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"duration":-5}}`,
			reject: "plugin usage value must be a finite non-negative number",
		},
		{
			name:   "wan2.2 duration far above its values",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":100000}`,
			reject: "plugin usage value exceeds the host limit",
		},
		{
			// A wrapped negative 64-bit integer.
			name:   "wan2.2 huge duration in metadata",
			model:  "wan2.2-i2v-spicy",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"duration":18446744073686646784}}`,
			reject: "plugin usage value exceeds the host limit",
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
			submission := submitThroughMulerouterPlugin(t, plugin, tc.model, tc.body)
			if tc.reject != "" {
				assert.Contains(t, submission.Rejection, tc.reject)
				assert.NotContains(t, submission.Rejection, "carrothub")
				return
			}
			require.Empty(t, submission.Rejection)
			assert.Equal(t, mulerouterSubmission{URL: tc.url, Body: tc.upload, Ratios: tc.ratios, Action: mulerouterTaskActions[tc.model]}, submission)
		})
	}
}

// A video submission on POST /v1/videos sends this upstream request and bills
// these multipliers, or is refused before it is charged. The cases cover each
// default and every enum value.
//
// /v1/videos takes the OpenAI video fields: seconds is the duration, size
// (WIDTHxHEIGHT) the resolution of its short side, and input_reference or
// image the URL of the first frame. Vendor parameters count at the top level
// and under metadata alike, two spellings of one parameter must agree, and a
// field the endpoint does not take is refused. /v1/tasks/mulerouter instead
// sends the client's body as written, with metadata winning: for the cases
// where that differs it sends and bills tasksUpload and tasksRatios, or
// refuses with tasksReject.
func TestMulerouterPluginVideosAPISubmissions(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	const wanVideo = mulerouterBaseURL + "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation"
	cases := []struct {
		name   string
		body   string
		upload map[string]any
		ratios map[string]float64
		reject string
		// tasksUpload/tasksRatios or tasksReject set what POST
		// /v1/tasks/mulerouter does when it differs from /v1/videos. A case
		// that sets none of them expects the same as /v1/videos: upload and
		// ratios, or the refusal reject.
		tasksUpload map[string]any
		tasksRatios map[string]float64
		tasksReject string
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
			name:   "missing prompt",
			body:   `{"model":"wan2.2-i2v-spicy","image":"https://cdn/in.png"}`,
			reject: "prompt is required",
		},
		{
			name:        "seconds is the duration",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","seconds":"8"}`,
			upload:      map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
			ratios:      map[string]float64{"seconds": 8, "resolution": 1},
			tasksReject: "parameter(s) seconds affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "seconds outside the duration values",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","seconds":"6"}`,
			reject:      "invalid value 6 for duration, allowed: 5, 8",
			tasksReject: "parameter(s) seconds affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "size is the resolution of its short side",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","size":"1280x720"}`,
			upload:      map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "resolution": "720p"},
			ratios:      map[string]float64{"seconds": 5, "resolution": 2},
			tasksReject: "parameter(s) size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			// The OpenAI-style example in guides/user/image-video-generation.md.
			name:        "a portrait size and seconds bill 8 seconds at 480p",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"镜头缓缓推进，人物微笑","image":"https://example.com/first-frame.png","seconds":"8","size":"480x832"}`,
			upload:      map[string]any{"prompt": "镜头缓缓推进，人物微笑", "image": "https://example.com/first-frame.png", "duration": float64(8), "resolution": "480p"},
			ratios:      map[string]float64{"seconds": 8, "resolution": 1},
			tasksReject: "parameter(s) seconds, size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "a size whose short side has no resolution",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","size":"1920x1080"}`,
			reject:      `invalid value "1080p" for resolution, allowed: 480p, 720p`,
			tasksReject: "parameter(s) size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "a size that is not WIDTHxHEIGHT",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","size":"large"}`,
			reject:      `size must be WIDTHxHEIGHT, such as 1280x720, got "large"`,
			tasksReject: "parameter(s) size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "input_reference is the input image",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","input_reference":"https://cdn/in.png"}`,
			upload:      map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			ratios:      map[string]float64{"seconds": 5, "resolution": 1},
			tasksUpload: map[string]any{"prompt": "orbit", "input_reference": "https://cdn/in.png"},
			tasksRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:        "input_reference that is not a URL",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","input_reference":{"file_id":"file_1"}}`,
			reject:      "input_reference must be an image URL",
			tasksUpload: map[string]any{"prompt": "orbit", "input_reference": map[string]any{"file_id": "file_1"}},
			tasksRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			// The top-level vendor parameter example in
			// guides/user/image-video-generation.md.
			name:   "top-level vendor parameters reach upstream and bill as written",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"镜头缓缓推进，人物微笑","image":"https://example.com/first-frame.png","resolution":"720p","seed":7,"prompt_extend":false,"last_image":"https://example.com/last-frame.png"}`,
			upload: map[string]any{"prompt": "镜头缓缓推进，人物微笑", "image": "https://example.com/first-frame.png", "resolution": "720p", "seed": float64(7), "prompt_extend": false, "last_image": "https://example.com/last-frame.png"},
			ratios: map[string]float64{"seconds": 5, "resolution": 2},
		},
		{
			name:   "a top-level unknown resolution",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","resolution":"1080p"}`,
			reject: `invalid value "1080p" for resolution, allowed: 480p, 720p`,
		},
		{
			name:        "seconds and duration that agree",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","seconds":"8","duration":8}`,
			upload:      map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
			ratios:      map[string]float64{"seconds": 8, "resolution": 1},
			tasksReject: "parameter(s) seconds affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "seconds and duration that disagree",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","seconds":"8","duration":5}`,
			reject:      "duration and seconds give different values",
			tasksReject: "parameter(s) seconds affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "size and resolution that disagree",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","size":"1280x720","metadata":{"resolution":"480p"}}`,
			reject:      "size and metadata.resolution give different values",
			tasksReject: "parameter(s) size affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "input_reference and image that disagree",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/a.png","input_reference":"https://cdn/b.png"}`,
			reject:      "image and input_reference give different values",
			tasksUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/a.png", "input_reference": "https://cdn/b.png"},
			tasksRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:        "the top level and metadata that disagree",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","resolution":"480p","metadata":{"resolution":"720p"}}`,
			reject:      "resolution and metadata.resolution give different values",
			tasksUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "resolution": "720p"},
			tasksRatios: map[string]float64{"seconds": 5, "resolution": 2},
		},
		{
			name:   "the top level and metadata that agree",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","resolution":"720p","metadata":{"resolution":"720p"}}`,
			upload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "resolution": "720p"},
			ratios: map[string]float64{"seconds": 5, "resolution": 2},
		},
		{
			name:        "a field the endpoint does not take",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","mode":"std"}`,
			reject:      "parameter(s) mode are not supported by this model; remove them",
			tasksUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "mode": "std"},
			tasksRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
		{
			name:        "an unpriced cost field the endpoint does not take",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":{"fps":24}}`,
			reject:      "parameter(s) fps are not supported by this model; remove them",
			tasksReject: "parameter(s) fps affect the price of this model and are not enabled on this endpoint; remove them",
		},
		{
			name:        "a string duration is sent as a number",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":"8"}`,
			upload:      map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": float64(8)},
			ratios:      map[string]float64{"seconds": 8, "resolution": 1},
			tasksUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png", "duration": "8"},
			tasksRatios: map[string]float64{"seconds": 8, "resolution": 1},
		},
		{
			name:   "a fractional duration",
			body:   `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","duration":5.5}`,
			reject: "duration must be an integer, got 5.5",
		},
		{
			name:        "metadata sent as a JSON string",
			body:        `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png","metadata":"{\"resolution\":\"720p\"}"}`,
			reject:      "metadata must be a JSON object",
			tasksUpload: map[string]any{"prompt": "orbit", "image": "https://cdn/in.png"},
			tasksRatios: map[string]float64{"seconds": 5, "resolution": 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			videos := submitThroughMulerouterVideosAPI(t, plugin, "wan2.2-i2v-spicy", tc.body)
			tasksEntry := submitThroughMulerouterPlugin(t, plugin, "wan2.2-i2v-spicy", tc.body)
			expected := mulerouterSubmission{URL: wanVideo, Body: tc.upload, Ratios: tc.ratios, Action: "image_to_video"}
			if tc.reject != "" {
				assert.Contains(t, videos.Rejection, tc.reject)
				assert.NotContains(t, videos.Rejection, "carrothub")
			} else {
				assert.Equal(t, expected, videos)
			}

			// No expectation of its own: /v1/tasks/mulerouter matches /v1/videos.
			tasksReject := tc.tasksReject
			if tasksReject == "" && tc.tasksUpload == nil {
				tasksReject = tc.reject
			}
			if tasksReject != "" {
				assert.Contains(t, tasksEntry.Rejection, tasksReject)
				assert.NotContains(t, tasksEntry.Rejection, "carrothub")
				return
			}
			if tc.tasksUpload != nil {
				expected = mulerouterSubmission{URL: wanVideo, Body: tc.tasksUpload, Ratios: tc.tasksRatios, Action: "image_to_video"}
			}
			assert.Equal(t, expected, tasksEntry)
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

// The plugin reads MuleRouter's submit and poll responses in both shapes its
// documentation gives (the carrothub endpoint YAML and the async task guide):
// the task id, the gateway status, the failure reason and the result URL.
func TestMulerouterPluginReadsTasks(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	pluginAdaptor := taskplugin.New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k", UpstreamModelName: "carrothub/z-image-spicy/generation"},
		OriginModelName: "z-image-spicy",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	pluginAdaptor.Init(info)

	t.Run("submit response", func(t *testing.T) {
		submit := func(body string) (string, *taskdto.TaskError) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/mulerouter", nil)
			resp := &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			parsed, taskErr := pluginAdaptor.ParseResponse(c, resp, info)
			if taskErr != nil {
				return "", taskErr
			}
			return parsed.UpstreamTaskID, nil
		}
		accepted := `{"task_info":{"id":"up-1","status":"pending","created_at":"2026-10-20T00:00:00Z"}}`
		id, taskErr := submit(accepted)
		require.Nil(t, taskErr)
		assert.Equal(t, "up-1", id)

		_, taskErr = submit(`{"task_info":{"status":"pending"}}`)
		require.NotNil(t, taskErr)

		// A submission that fails synchronously carries its error where a
		// failed poll does: under task_info (carrothub ErrorResponse) or at the
		// top level (the async task guide).
		for _, tc := range []struct{ name, body, reason string }{
			{
				"error under task_info",
				`{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"failed","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:00:00Z","error":{"code":3001,"title":"Invalid Request","detail":"Task execution failed"}}}`,
				"Invalid Request: Task execution failed (error code 3001)",
			},
			{
				"error at the top level",
				`{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"failed","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:02:13Z"},"error":{"title":"External service execution failed","error_code":3005,"detail":"Kling task failed: video generation failed due to prompt violation"}}`,
				"External service execution failed: Kling task failed: video generation failed due to prompt violation (error code 3005)",
			},
			{
				// A refusal without a task carries no failed status, only the error.
				"error without a task",
				`{"error":{"title":"Invalid Request","error_code":3001,"detail":"prompt must not be empty"}}`,
				"Invalid Request: prompt must not be empty (error code 3001)",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, taskErr := submit(tc.body)
				require.NotNil(t, taskErr)
				assert.Contains(t, taskErr.Message, tc.reason)
			})
		}
	})

	t.Run("query request", func(t *testing.T) {
		for _, tc := range []struct{ publicModel, upstreamModel, path string }{
			{"z-image-spicy", "carrothub/z-image-spicy/generation", "/vendors/carrothub/v1/z-image-spicy/generation/up-1"},
			{"wan2.2-i2v-spicy", "carrothub/wan2.2-i2v-spicy/generation", "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation/up-1"},
		} {
			var requested *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested = r
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"task_info":{"id":"up-1","status":"processing"}}`)
			}))
			task := &model.Task{TaskID: "task_public", Properties: model.Properties{OriginModelName: tc.publicModel, UpstreamModelName: tc.upstreamModel}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "up-1"}}
			resp, err := pluginAdaptor.FetchTask(server.URL, "k", task, "")
			require.NoError(t, err)
			resp.Body.Close()
			server.Close()
			require.NotNil(t, requested)
			assert.Equal(t, http.MethodGet, requested.Method)
			assert.Equal(t, tc.path, requested.URL.Path)
			assert.Equal(t, "Bearer k", requested.Header.Get("Authorization"))
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
			// Status words of the carrothub endpoint YAML: pending, processing,
			// completed, failed. The wan2.2-i2v-spicy YAML also names retrying.
			{"pending", `{"task_info":{"id":"up-1","status":"pending"}}`, model.TaskStatusSubmitted, "", ""},
			{"processing", `{"task_info":{"id":"up-1","status":"processing"}}`, model.TaskStatusInProgress, "", ""},
			{"retrying", `{"task_info":{"id":"up-1","status":"retrying"}}`, model.TaskStatusInProgress, "", ""},
			{"completed image", `{"task_info":{"id":"up-1","status":"completed"},"images":["https://cdn/a.png","https://cdn/b.png"]}`, model.TaskStatusSuccess, "https://cdn/a.png", ""},
			{"completed video", `{"task_info":{"id":"up-1","status":"completed"},"videos":["https://cdn/a.mp4"]}`, model.TaskStatusSuccess, "https://cdn/a.mp4", ""},
			// Status words of the async task guide: queued, running, succeeded,
			// failed.
			{"queued", `{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"queued","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:00:00Z"}}`, model.TaskStatusSubmitted, "", ""},
			{"running", `{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"running","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:00:00Z"}}`, model.TaskStatusInProgress, "", ""},
			{"succeeded", `{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"succeeded","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:00:00Z"},"videos":["https://example.com/video-749337402830284.mp4"]}`, model.TaskStatusSuccess, "https://example.com/video-749337402830284.mp4", ""},
			// A success without any image, video or audio fails, so the host
			// refunds the task.
			{"success without artifacts", `{"task_info":{"id":"up-1","status":"succeeded"},"images":[],"videos":[],"audios":[]}`, model.TaskStatusFailure, "", "the task completed without producing any image, video or audio"},
			// The carrothub ErrorResponse puts the error under task_info, the
			// async task guide at the top level.
			{"failed with the error under task_info", `{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"failed","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:00:00Z","error":{"code":3001,"title":"Invalid Request","detail":"Task execution failed"}}}`, model.TaskStatusFailure, "", "Invalid Request: Task execution failed (error code 3001)"},
			{"failed with the error at the top level", `{"task_info":{"id":"123e4567-e89b-12d3-a456-426614174000","status":"failed","created_at":"2025-09-16T10:00:00Z","updated_at":"2025-09-16T10:02:13Z"},"error":{"title":"External service execution failed","error_code":3005,"detail":"Kling task failed: video generation failed due to prompt violation"}}`, model.TaskStatusFailure, "", "External service execution failed: Kling task failed: video generation failed due to prompt violation (error code 3005)"},
			{"failed with an empty error under task_info and one at the top level", `{"task_info":{"id":"up-1","status":"failed","error":{}},"error":{"title":"External service execution failed","error_code":3005,"detail":"upstream timeout"}}`, model.TaskStatusFailure, "", "External service execution failed: upstream timeout (error code 3005)"},
			{"failed with a string error under task_info and one at the top level", `{"task_info":{"id":"up-1","status":"failed","error":"internal"},"error":{"title":"External service execution failed","detail":"upstream timeout"}}`, model.TaskStatusFailure, "", "External service execution failed: upstream timeout"},
			{"failed with title only", `{"task_info":{"id":"up-1","status":"failed","error":{"title":"Invalid Request"}}}`, model.TaskStatusFailure, "", "Invalid Request"},
			{"failed with detail only", `{"error":{"detail":"Task execution failed"},"task_info":{"id":"up-1","status":"failed"}}`, model.TaskStatusFailure, "", "Task execution failed"},
			{"failed without an error", `{"task_info":{"id":"up-1","status":"failed"}}`, model.TaskStatusFailure, "", "task failed"},
			// An unknown status is a failed poll, never progress.
			{"unknown status", `{"task_info":{"id":"up-1","status":"paused"}}`, "UNKNOWN", "", "unknown task status: paused"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				result, err := pluginAdaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, []byte(tc.body))
				require.NoError(t, err)
				assert.Equal(t, string(tc.status), result.Status)
				assert.Equal(t, tc.url, result.Url)
				assert.Equal(t, tc.reason, result.Reason)
			})
		}
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
		Platform:  constant.TaskPlatform(mulerouterPluginKey),
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
// persisted data is the latest upstream poll body. The plugin renders each
// state as the Go adaptor's ConvertToOpenAIVideo did, which generated the
// expected objects, with two differences. A completed task no longer carries
// metadata.url, the MuleRouter result URL; the video comes from the content
// route. And the host sends completed_at only for a completed task, where the
// Go adaptor also stamped an unfinished task with its last update time.
func TestMulerouterPluginRendersOpenAIVideo(t *testing.T) {
	pluginConverter, ok := relay.GetTaskAdaptor(constant.TaskPlatform(mulerouterPluginKey)).(relaychannel.OpenAIVideoConverter)
	require.True(t, ok)

	videoTask := func(status model.TaskStatus, progress, failReason, data string) *model.Task {
		task := &model.Task{
			TaskID:      "task_public",
			Platform:    constant.TaskPlatform(mulerouterPluginKey),
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
		name       string
		status     model.TaskStatus
		progress   string
		failReason string
		data       string
		want       string
	}{
		{
			name:   "queued",
			status: model.TaskStatusSubmitted,
			data:   `{"task_info":{"id":"up-1","status":"pending","created_at":"2026-10-20T00:00:00Z"}}`,
			want:   `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"queued","progress":0,"created_at":1786370025}`,
		},
		{
			name:     "processing",
			status:   model.TaskStatusInProgress,
			progress: "30%",
			data:     `{"task_info":{"id":"up-1","status":"processing"}}`,
			want:     `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"in_progress","progress":30,"created_at":1786370025}`,
		},
		{
			// The video is downloaded from GET /v1/videos/{task_id}/content,
			// which the gateway proxies; the MuleRouter result URL would
			// reveal the vendor, carrothub and the upstream task id.
			name:     "completed hides the result URL",
			status:   model.TaskStatusSuccess,
			progress: "100%",
			data:     `{"task_info":{"id":"up-1","status":"completed"},"videos":["https://mule-router-assets.muleusercontent.com/u/carrothub/tasks/up-1/outputs/result_00.mp4"]}`,
			want:     `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"completed","progress":100,"created_at":1786370025,"completed_at":1786370124}`,
		},
		{
			name:       "failed carries the failure reason",
			status:     model.TaskStatusFailure,
			progress:   "100%",
			failReason: "bad_prompt: blocked",
			data:       `{"task_info":{"id":"up-1","status":"failed","error":{"title":"bad_prompt","detail":"blocked"}}}`,
			want:       `{"id":"task_public","object":"video","model":"wan2.2-i2v-spicy","status":"failed","progress":100,"created_at":1786370025,"error":{"message":"bad_prompt: blocked","code":"task_failed"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := videoTask(tc.status, tc.progress, tc.failReason, tc.data)
			pluginBody, err := pluginConverter.ConvertToOpenAIVideo(task)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(pluginBody))
		})
	}

	// GET /v1/videos/{task_id}/content streams the result URL, as the Go
	// channel's tasks did through their stored result URL.
	t.Run("content is the first result video", func(t *testing.T) {
		adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(mulerouterPluginKey))
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k"}})
		provider, ok := adaptor.(relaychannel.TaskContentRequestProvider)
		require.True(t, ok)
		artifacts, ok := adaptor.(relaychannel.TaskArtifactProvider)
		require.True(t, ok)

		done := videoTask(model.TaskStatusSuccess, "100%", "", `{"task_info":{"id":"up-1","status":"completed"},"videos":["https://cdn/out.mp4","https://cdn/alt.mp4"]}`)
		listed, err := artifacts.ListArtifacts(done)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, "video", listed[0].Key)
		assert.Equal(t, "video", listed[0].Type)
		descriptor, err := provider.BuildContentRequest(t.Context(), done, "video", relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
		require.NoError(t, err)
		assert.Equal(t, "https://cdn/out.mp4", descriptor.URL)
		assert.True(t, descriptor.Credentialless)

		running := videoTask(model.TaskStatusInProgress, "30%", "", `{"task_info":{"id":"up-1","status":"processing"}}`)
		listed, err = artifacts.ListArtifacts(running)
		require.NoError(t, err)
		assert.Empty(t, listed)
	})

	t.Run("an image task is not a video", func(t *testing.T) {
		task := videoTask(model.TaskStatusSuccess, "100%", "", `{"task_info":{"id":"up-1","status":"completed"},"images":["https://cdn/a.png"]}`)
		task.Properties = model.Properties{OriginModelName: "z-image-spicy", UpstreamModelName: "carrothub/z-image-spicy/generation"}
		_, err := pluginConverter.ConvertToOpenAIVideo(task)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "carrothub")
	})
}

// GET /v1/tasks/{task_id}/artifacts lists a completed image task's images as
// image-1, image-2, ... in the order MuleRouter returns them, and each
// content URL streams that image. A key past the last image, or one the task
// does not have, is not found. Video tasks keep their single "video" artifact
// (TestMulerouterPluginRendersOpenAIVideo).
func TestMulerouterPluginListsImageArtifacts(t *testing.T) {
	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(mulerouterPluginKey))
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: mulerouterBaseURL, ApiKey: "k"}})
	artifacts, ok := adaptor.(relaychannel.TaskArtifactProvider)
	require.True(t, ok)
	provider, ok := adaptor.(relaychannel.TaskContentRequestProvider)
	require.True(t, ok)

	imageTask := func(status model.TaskStatus, data string) *model.Task {
		return &model.Task{
			TaskID:      "task_public",
			Platform:    constant.TaskPlatform(mulerouterPluginKey),
			Status:      status,
			Properties:  model.Properties{OriginModelName: "z-image-spicy", UpstreamModelName: "carrothub/z-image-spicy/generation"},
			PrivateData: model.TaskPrivateData{UpstreamTaskID: "up-1"},
			Data:        []byte(data),
		}
	}

	done := imageTask(model.TaskStatusSuccess, `{"task_info":{"id":"up-1","status":"completed"},"images":["https://cdn/a.png","https://cdn/b.png"]}`)
	listed, err := artifacts.ListArtifacts(done)
	require.NoError(t, err)
	assert.Equal(t, []relaychannel.TaskArtifact{{Key: "image-1", Type: "image"}, {Key: "image-2", Type: "image"}}, listed)

	for _, tc := range []struct{ key, url string }{
		{"image-1", "https://cdn/a.png"},
		{"image-2", "https://cdn/b.png"},
		{"image-3", ""},
		{"image-0", ""},
		{"image-01", ""},
		{"image-1.0", ""},
		{"image-1x", ""},
		{"image-", ""},
		{"video", ""},
		{"image", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			descriptor, err := provider.BuildContentRequest(t.Context(), done, tc.key, relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
			if tc.url == "" {
				require.ErrorContains(t, err, "artifact_not_found")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.url, descriptor.URL)
			assert.Equal(t, http.MethodGet, descriptor.Method)
			assert.True(t, descriptor.Credentialless, "MuleRouter result links are public")
		})
	}

	for _, tc := range []struct {
		name string
		task *model.Task
	}{
		{"still running", imageTask(model.TaskStatusInProgress, `{"task_info":{"id":"up-1","status":"processing"}}`)},
		{"completed without results", imageTask(model.TaskStatusSuccess, `{"task_info":{"id":"up-1","status":"completed"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed, err := artifacts.ListArtifacts(tc.task)
			require.NoError(t, err)
			assert.Empty(t, listed)
		})
	}
}

// Callers submit MuleRouter tasks on POST /v1/tasks/mulerouter. The entry pins
// the plugin before channel selection, so Distribute picks the type-61 channel
// bound to it for a public model name, and not a channel of another plugin or
// another type that offers the same model at a higher priority. The request
// goes through the registered task routes with a real API token, and the
// submission reaches the bound channel's upstream and is filed under it.
// Selection runs from the channel cache, as on a gateway with Redis.
func TestMulerouterTasksEntryReachesTheBoundChannel(t *testing.T) {
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousSQLitePath, previousMaster := common.SQLitePath, common.IsMasterNode
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousBatch, previousLogConsume := common.BatchUpdateEnabled, common.LogConsumeEnabled
	previousPrices := ratio_setting.ModelPrice2JSONString()
	// InitDB also sets the column names token lookup quotes.
	common.SQLitePath, common.IsMasterNode = t.TempDir()+"/tasks-entry.db", false
	t.Setenv("SQL_DSN", "")
	require.NoError(t, model.InitDB())
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Task{}, &model.Log{}, &model.UserSubscription{}))
	model.LOG_DB = db
	common.RedisEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"z-image-spicy":0.013,"wan2.2-i2v-spicy":0.02}`))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.SQLitePath, common.IsMasterNode = previousSQLitePath, previousMaster
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.BatchUpdateEnabled, common.LogConsumeEnabled = previousBatch, previousLogConsume
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
		sqlDB, closeErr := db.DB()
		require.NoError(t, closeErr)
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, db.Create(&model.User{
		Id: 1, Username: "tasks-entry", Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		Quota: 100_000_000, Group: "default", AuthVersion: 1,
	}).Error)
	require.NoError(t, db.Create(&model.Token{
		Id: 1, UserId: 1, Key: "tasksentry", Name: "tasks entry", Status: common.TokenStatusEnabled,
		ExpiredTime: -1, UnlimitedQuota: true,
	}).Error)

	var submitted []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted = append(submitted, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task_info":{"id":"up-1","status":"pending"}}`)
	}))
	t.Cleanup(upstream.Close)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a channel not bound to mulerouter received %s %s", r.Method, r.URL.Path)
		http.Error(w, "wrong channel", http.StatusInternalServerError)
	}))
	t.Cleanup(elsewhere.Close)

	mapping := mulerouterStagingModelMapping
	channels := []*model.Channel{
		{Id: 1, Type: constant.ChannelTypeTaskPlugin, Name: "mulerouter", BaseURL: &upstream.URL, Models: "z-image-spicy,wan2.2-i2v-spicy", ModelMapping: &mapping, Priority: common.GetPointer(int64(0))},
		{Id: 2, Type: constant.ChannelTypeTaskPlugin, Name: "another plugin", BaseURL: &elsewhere.URL, Models: "z-image-spicy,wan2.2-i2v-spicy", Priority: common.GetPointer(int64(10))},
		{Id: 3, Type: constant.ChannelTypeOpenAI, Name: "openai", BaseURL: &elsewhere.URL, Models: "z-image-spicy,wan2.2-i2v-spicy", Priority: common.GetPointer(int64(20))},
	}
	channels[0].SetSetting(kitdto.ChannelSettings{TaskPluginKey: mulerouterPluginKey})
	channels[1].SetSetting(kitdto.ChannelSettings{TaskPluginKey: "sora"})
	for _, channel := range channels {
		channel.Key, channel.Status, channel.Group = "test-key", common.ChannelStatusEnabled, "default"
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(db))
	}

	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	t.Cleanup(model.InitChannelCache)
	engine := gin.New()
	router.SetTaskRouter(engine)
	for _, tc := range []struct {
		publicModel string
		body        string
		path        string
	}{
		{"z-image-spicy", `{"model":"z-image-spicy","prompt":"a cat"}`, "/vendors/carrothub/v1/z-image-spicy/generation"},
		{"wan2.2-i2v-spicy", `{"model":"wan2.2-i2v-spicy","prompt":"orbit","image":"https://cdn/in.png"}`, "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation"},
	} {
		t.Run(tc.publicModel, func(t *testing.T) {
			submitted = nil
			request := httptest.NewRequest(http.MethodPost, "/v1/tasks/mulerouter", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer sk-tasksentry")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, []string{http.MethodPost + " " + tc.path}, submitted)
			var task model.Task
			require.NoError(t, db.Order("id desc").First(&task).Error)
			assert.Equal(t, 1, task.ChannelId)
			assert.Equal(t, constant.TaskPlatform(mulerouterPluginKey), task.Platform)
			assert.Equal(t, tc.publicModel, task.Properties.OriginModelName)
			assert.Equal(t, mulerouterTaskActions[tc.publicModel], task.Action)
		})
	}
}
