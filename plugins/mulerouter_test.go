package plugins_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
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

func TestMulerouterPluginIsABuiltInPluginOnTaskPluginChannelsOnly(t *testing.T) {
	plugin, found := jsplugin.DefaultRegistry.Generation().Get("mulerouter")
	require.True(t, found)
	assert.Empty(t, plugin.Meta.ChannelTypes, "type-64 channels and their tasks stay with the Go adaptor")
	assert.Empty(t, plugin.Meta.Routes)
	assert.Empty(t, plugin.Meta.Protocols)
	assert.Equal(t, mulerouterBaseURL, plugin.Meta.BaseURL)
	assert.Contains(t, plugin.Meta.Models, "carrothub/z-image-spicy/generation")
}

// Every case runs through the Go MuleRouter adaptor (billing_vars and request
// body construction) and through the plugin, and both must agree on the
// upstream request and the billing multipliers, or both refuse it. Later
// MuleRouter endpoints add their cases to this table.
func TestMulerouterPluginMatchesGoAdaptor(t *testing.T) {
	plugin := newMulerouterPlugin(t)
	const zImage = mulerouterBaseURL + "/vendors/carrothub/v1/z-image-spicy/generation"

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
			name:   "z-image width above the bound",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"width":4096}}`,
			reject: "width must be <= 1536",
		},
		{
			name:   "z-image height below the bound",
			model:  "z-image-spicy",
			body:   `{"model":"z-image-spicy","prompt":"a cat","metadata":{"height":100}}`,
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goSubmission := submitThroughGoMulerouter(t, tc.model, tc.body)
			pluginSubmission := submitThroughMulerouterPlugin(t, plugin, tc.model, tc.body)
			if tc.reject != "" {
				assert.Equal(t, tc.reject, goSubmission.Rejection)
				assert.Contains(t, pluginSubmission.Rejection, tc.reject)
				assert.NotContains(t, pluginSubmission.Rejection, "carrothub")
				return
			}
			require.Empty(t, goSubmission.Rejection)
			require.Empty(t, pluginSubmission.Rejection)
			assert.Equal(t, mulerouterSubmission{URL: tc.url, Body: tc.upload, Ratios: tc.ratios}, goSubmission)
			assert.Equal(t, goSubmission, pluginSubmission)
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
		requests := make([]*http.Request, 0, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests = append(requests, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"task_info":{"id":"up-1","status":"processing"}}`)
		}))
		defer server.Close()
		task := &model.Task{TaskID: "task_public", Properties: model.Properties{OriginModelName: "z-image-spicy", UpstreamModelName: "carrothub/z-image-spicy/generation"}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "up-1"}}
		for _, adaptor := range []relaychannel.TaskAdaptor{goAdaptor, pluginAdaptor} {
			resp, err := adaptor.FetchTask(server.URL, "k", task, "")
			require.NoError(t, err)
			resp.Body.Close()
		}
		require.Len(t, requests, 2)
		for _, r := range requests {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/vendors/carrothub/v1/z-image-spicy/generation/up-1", r.URL.Path)
			assert.Equal(t, "Bearer k", r.Header.Get("Authorization"))
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
