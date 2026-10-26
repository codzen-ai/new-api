package plugins_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mgtvBaseURL = "https://maas-api-pub.cloud.mgtv.com"
	// mgtvPluginKey is the plugin's key, which is also the task platform of
	// the tasks it serves.
	mgtvPluginKey = "mgtv"
	mgtvSubmitURL = mgtvBaseURL + "/v1/videos"
)

// mgtvSubmission is what a channel does with one client submission: the
// upstream request it would send and the usage facts it would bill, or the
// reason it refuses the request before charging.
type mgtvSubmission struct {
	URL           string
	Authorization string
	Body          map[string]any
	Facts         map[string]any
	Action        string
	Rejection     string
}

func newMgtvPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source(mgtvPluginKey)
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: mgtvPluginKey})
	require.NoError(t, err)
	return plugin
}

// mgtvPluginChannel is a type-61 Task Plugin channel bound to the plugin.
func mgtvPluginChannel() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeTaskPlugin, ChannelBaseUrl: mgtvBaseURL, ApiKey: "k"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
}

// mgtvSubmitContext is a client request on path with the channel model
// mapping, if any, applied the way the task relay applies it.
func mgtvSubmitContext(t *testing.T, path, model, modelMapping, body string, info *relaycommon.RelayInfo) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if modelMapping != "" {
		c.Set("model_mapping", modelMapping)
	}
	info.OriginModelName = model
	info.UpstreamModelName = model
	require.NoError(t, helper.ModelMappedHelper(c, info, nil))
	return c
}

// submitToMgtvPlugin runs a prepared client request through the plugin task
// adaptor as the task relay does.
func submitToMgtvPlugin(t *testing.T, plugin *jsplugin.LoadedPlugin, c *gin.Context, info *relaycommon.RelayInfo) mgtvSubmission {
	t.Helper()
	adaptor := taskplugin.New(plugin)
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		return mgtvSubmission{Rejection: taskErr.Message}
	}
	facts, err := adaptor.ExtractUsageFactsValidated(c, info)
	if err != nil {
		return mgtvSubmission{Rejection: err.Error()}
	}
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	reader, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	upstream := httptest.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, adaptor.BuildRequestHeader(c, upstream, info))
	return mgtvSubmission{
		URL:           url,
		Authorization: upstream.Header.Get("Authorization"),
		Body:          readMulerouterBody(t, reader),
		Facts:         facts,
		Action:        info.Action,
	}
}

// submitThroughMgtvVideosAPI is a client request on POST /v1/videos: the host
// has pinned the plugin's openai_video binding for the model, and channel
// selection has decoded the request once already.
func submitThroughMgtvVideosAPI(t *testing.T, plugin *jsplugin.LoadedPlugin, model, modelMapping, body string) mgtvSubmission {
	t.Helper()
	info := mgtvPluginChannel()
	c := mgtvSubmitContext(t, "/v1/videos", model, modelMapping, body, info)
	var value any
	require.NoError(t, common.UnmarshalJsonStr(body, &value))
	protocolRequest := jsplugin.ProtocolRequestContext{
		RouteRequestContext: jsplugin.RouteRequestContext{Body: map[string]any{"kind": "json", "value": value}},
		Protocol:            "openai_video", Operation: "create", Model: model, UpstreamModel: info.UpstreamModelName,
	}
	if _, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, protocolRequest.JSValueFor(plugin.Meta)); err != nil {
		return mgtvSubmission{Rejection: err.Error()}
	}
	c.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{Plugin: plugin, Protocol: "openai_video", Model: model, MappedModel: info.UpstreamModelName})
	c.Set(jsplugin.ContextKeyProtocolRequest, protocolRequest)
	return submitToMgtvPlugin(t, plugin, c, info)
}

// submitThroughMgtvTasksEntry is a client request on POST /v1/tasks/mgtv,
// which the host serves for every plugin and which hands the plugin the
// client's whole JSON body without calling its decoder.
func submitThroughMgtvTasksEntry(t *testing.T, plugin *jsplugin.LoadedPlugin, model, body string) mgtvSubmission {
	t.Helper()
	info := mgtvPluginChannel()
	c := mgtvSubmitContext(t, "/v1/tasks/mgtv", model, "", body, info)
	var request map[string]any
	require.NoError(t, common.UnmarshalBodyReusable(c, &request))
	c.Set("task_request", request)
	return submitToMgtvPlugin(t, plugin, c, info)
}

func TestMgtvPluginIsABuiltInPluginOnTaskPluginChannelsOnly(t *testing.T) {
	plugin, found := jsplugin.DefaultRegistry.Generation().Get(mgtvPluginKey)
	require.True(t, found)
	assert.Empty(t, plugin.Meta.ChannelTypes, "the plugin serves only Task Plugin channels bound to it")
	assert.Empty(t, plugin.Meta.Routes)
	require.Len(t, plugin.Meta.Protocols, 1)
	assert.Equal(t, "openai_video", plugin.Meta.Protocols[0].Name)
	assert.Equal(t, mgtvBaseURL, plugin.Meta.BaseURL)
	// The plugin declares the public model names, never the MaaS ones.
	assert.ElementsMatch(t, []string{"seedance-2.5", "seedance-2.0-pro", "seedance-2.0-fast", "seedance-2.0-mini"}, plugin.Meta.Models)
}

// Every model prices the same three facts, and the pricing page lists only
// the resolution tiers the model offers, each with a 5-second example.
func TestMgtvPluginUsageProfiles(t *testing.T) {
	plugin := newMgtvPlugin(t)
	for _, tc := range []struct {
		model       string
		resolutions []string
	}{
		{"seedance-2.5", []string{"480p", "720p", "1080p"}},
		{"seedance-2.0-pro", []string{"480p", "720p", "1080p", "4k"}},
		{"seedance-2.0-fast", []string{"480p", "720p"}},
		{"seedance-2.0-mini", []string{"480p", "720p"}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			schema, examples := plugin.Meta.UsageForModel(tc.model)
			assert.ElementsMatch(t, []string{"tokens", "resolution", "video_input"}, keysOf(schema))
			assert.Equal(t, "token", schema["tokens"].Unit)
			assert.Equal(t, "Billing token unit price", schema["tokens"].Description["en"])
			assert.Equal(t, tc.resolutions, schema["resolution"].Enum)
			assert.Equal(t, []string{"none", "video"}, schema["video_input"].Enum)
			require.Len(t, examples, len(tc.resolutions))
			for _, example := range examples {
				assert.Contains(t, tc.resolutions, example.Facts["resolution"], example.Label)
				assert.Equal(t, "none", example.Facts["video_input"], example.Label)
			}
			assert.Equal(t, "720p · 5s", examples[1].Label)
			assert.EqualValues(t, 108000, examples[1].Facts["tokens"])
		})
	}
}

// A text-to-video submission on POST /v1/videos sends this MaaS request and
// bills these usage facts, or is refused before it is charged. The token
// estimate is seconds × width × height × 24 / 1024 at the tier's 16:9
// max-pixel size: 480p 854×480, 720p 1280×720, 1080p 1920×1080, 4k 3840×2160.
// POST /v1/tasks/mgtv hands the plugin the same body without the decoder and
// must send and bill exactly the same.
func TestMgtvPluginTextToVideoSubmissions(t *testing.T) {
	plugin := newMgtvPlugin(t)
	cases := []struct {
		name   string
		model  string
		body   string
		upload map[string]any
		facts  map[string]any
		reject string
	}{
		{
			// The model picks the length; the reservation covers 30 seconds.
			name:   "2.5 defaults send -1 at 720p and reserve 30 seconds",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat"}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(-1), "extra_body": map[string]any{"resolution": "720p"}},
			facts:  map[string]any{"tokens": float64(648000), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "2.5 takes -1 written by the caller",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","seconds":"-1","resolution":"1080p"}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(-1), "extra_body": map[string]any{"resolution": "1080p"}},
			facts:  map[string]any{"tokens": float64(30 * 48600), "resolution": "1080p", "video_input": "none"},
		},
		{
			name:   "2.5 at its longest duration",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","duration":30}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(30), "extra_body": map[string]any{"resolution": "720p"}},
			facts:  map[string]any{"tokens": float64(30 * 21600), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "2.0 pro defaults to 5 seconds at 720p",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat"}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(5), "extra_body": map[string]any{"resolution": "720p"}},
			facts:  map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "2.0 pro generates 4K",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","resolution":"4K"}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(5), "extra_body": map[string]any{"resolution": "4K"}},
			facts:  map[string]any{"tokens": float64(5 * 194400), "resolution": "4k", "video_input": "none"},
		},
		{
			name:   "2.0 fast at 480p",
			model:  "seedance-2.0-fast",
			body:   `{"model":"seedance-2.0-fast","prompt":"a cat","resolution":"480p","seconds":"15"}`,
			upload: map[string]any{"model": "msd-2-0-fast-260128", "prompt": "a cat", "seconds": float64(15), "extra_body": map[string]any{"resolution": "480p"}},
			facts:  map[string]any{"tokens": float64(15 * 9607.5), "resolution": "480p", "video_input": "none"},
		},
		{
			// The staging verification request of spec #48: about 38,000 tokens.
			name:   "2.0 mini 4 seconds at 480p",
			model:  "seedance-2.0-mini",
			body:   `{"model":"seedance-2.0-mini","prompt":"a cat","size":"854x480","seconds":"4"}`,
			upload: map[string]any{"model": "msd-2-0-mini-260615", "prompt": "a cat", "seconds": float64(4), "extra_body": map[string]any{"resolution": "480p", "aspectRatio": "16:9"}},
			facts:  map[string]any{"tokens": float64(38430), "resolution": "480p", "video_input": "none"},
		},
		{
			name:   "a portrait size names 720p and 9:16",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","size":"720x1280"}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(5), "extra_body": map[string]any{"resolution": "720p", "aspectRatio": "9:16"}},
			facts:  map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "a 2160 short side names 4K",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","size":"3840x2160"}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(5), "extra_body": map[string]any{"resolution": "4K", "aspectRatio": "16:9"}},
			facts:  map[string]any{"tokens": float64(5 * 194400), "resolution": "4k", "video_input": "none"},
		},
		{
			name:   "size agrees with resolution and aspect_ratio",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","size":"1280x720","resolution":"720p","aspect_ratio":"16:9","seconds":5}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(5), "extra_body": map[string]any{"resolution": "720p", "aspectRatio": "16:9"}},
			facts:  map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "2.5 takes the adaptive aspect ratio and mov",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","aspect_ratio":"adaptive","output_format":"mov","seconds":4}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(4), "extra_body": map[string]any{"resolution": "720p", "aspectRatio": "adaptive", "output_format": "mov"}},
			facts:  map[string]any{"tokens": float64(4 * 21600), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "with_audio false and seed 0 reach MaaS",
			model:  "seedance-2.0-mini",
			body:   `{"model":"seedance-2.0-mini","prompt":"a cat","with_audio":false,"seed":0}`,
			upload: map[string]any{"model": "msd-2-0-mini-260615", "prompt": "a cat", "seconds": float64(5), "extra_body": map[string]any{"resolution": "720p", "withAudio": false, "seed": float64(0)}},
			facts:  map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"},
		},
		{
			name:   "vendor parameters under metadata count",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","metadata":{"duration":8,"resolution":"1080p","aspect_ratio":"1:1","with_audio":true,"seed":42}}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(8), "extra_body": map[string]any{"resolution": "1080p", "aspectRatio": "1:1", "withAudio": true, "seed": float64(42)}},
			facts:  map[string]any{"tokens": float64(8 * 48600), "resolution": "1080p", "video_input": "none"},
		},
		{
			name:   "the top level and metadata that agree",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","seconds":"6","metadata":{"duration":6}}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(6), "extra_body": map[string]any{"resolution": "720p"}},
			facts:  map[string]any{"tokens": float64(6 * 21600), "resolution": "720p", "video_input": "none"},
		},
		{name: "seconds and duration that disagree", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","seconds":"8","duration":5}`, reject: "duration and seconds give different values"},
		{name: "the top level and metadata that disagree", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","resolution":"480p","metadata":{"resolution":"720p"}}`, reject: "resolution and metadata.resolution give different values"},
		{name: "size and resolution that disagree", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","size":"1280x720","resolution":"480p"}`, reject: "resolution and size give different values"},
		{name: "a fractional duration", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","seconds":5.5}`, reject: "seconds must be a whole number, got 5.5"},
		{name: "a duration that is not a number", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","duration":"five"}`, reject: `duration must be a whole number, got "five"`},
		{name: "2.0 below its shortest duration", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","seconds":3}`, reject: "seconds must be between 4 and 15 seconds"},
		{name: "2.0 above its longest duration", model: "seedance-2.0-mini", body: `{"model":"seedance-2.0-mini","prompt":"a cat","metadata":{"duration":16}}`, reject: "metadata.duration must be between 4 and 15 seconds"},
		{name: "2.0 refuses -1", model: "seedance-2.0-fast", body: `{"model":"seedance-2.0-fast","prompt":"a cat","seconds":-1}`, reject: "seconds must be between 4 and 15 seconds"},
		{name: "2.5 above its longest duration", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","seconds":31}`, reject: "seconds must be -1 or between 4 and 30 seconds"},
		{name: "2.5 refuses 0", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","duration":0}`, reject: "duration must be -1 or between 4 and 30 seconds"},
		{name: "a huge duration", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","metadata":{"duration":18446744073686646784}}`, reject: "metadata.duration must be -1 or between 4 and 30 seconds"},
		{name: "fast has no 1080p", model: "seedance-2.0-fast", body: `{"model":"seedance-2.0-fast","prompt":"a cat","resolution":"1080p"}`, reject: `resolution must be one of 480p, 720p on this model, got "1080p"`},
		{name: "mini has no 1080p size", model: "seedance-2.0-mini", body: `{"model":"seedance-2.0-mini","prompt":"a cat","size":"1920x1080"}`, reject: `resolution must be one of 480p, 720p on this model, got "1080p"`},
		{name: "2.5 has no 4K", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","resolution":"4k"}`, reject: `resolution must be one of 480p, 720p, 1080p on this model, got "4k"`},
		{name: "an unknown resolution", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","resolution":"8k"}`, reject: `resolution must be one of 480p, 720p, 1080p, 4k on this model, got "8k"`},
		{name: "adaptive is only on 2.5", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","aspect_ratio":"adaptive"}`, reject: `aspect_ratio must be one of 16:9, 4:3, 1:1, 3:4, 9:16, 21:9 on this model, got "adaptive"`},
		{name: "a size with no resolution tier", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","size":"1000x700"}`, reject: "size 1000x700 matches no resolution and aspect ratio"},
		{name: "a size with no aspect ratio", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","size":"1000x720"}`, reject: "size 1000x720 matches no resolution and aspect ratio"},
		{name: "a size that is not WIDTHxHEIGHT", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","size":"large"}`, reject: `size must be WIDTHxHEIGHT, such as 1280x720, got "large"`},
		{name: "with_audio that is not a boolean", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","with_audio":"yes"}`, reject: "with_audio must be true or false"},
		{name: "output_format outside mp4 and mov", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","output_format":"avi"}`, reject: `output_format must be one of mp4, mov, got "avi"`},
		{name: "output_format is only on 2.5", model: "seedance-2.0-pro", body: `{"model":"seedance-2.0-pro","prompt":"a cat","output_format":"mp4"}`, reject: "parameter(s) output_format are not supported by this model; remove them"},
		{
			// asset_id would reference another caller's asset on the shared
			// upstream account; audio roles, web search and thumbnails have no
			// price yet.
			name:   "unpriced and unsafe fields are refused and named",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","asset_id":"a1","web_search":true,"thumbnail_format":"png","metadata":{"fps":24,"asset_id":"a2"}}`,
			reject: "parameter(s) asset_id, fps, thumbnail_format, web_search are not supported by this model; remove them",
		},
		{name: "a missing prompt", model: "seedance-2.5", body: `{"model":"seedance-2.5","seconds":5}`, reject: "prompt is required"},
		{name: "a blank prompt", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"  "}`, reject: "prompt is required"},
		{name: "metadata sent as a JSON string", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","metadata":"{}"}`, reject: "metadata must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for entry, submission := range map[string]mgtvSubmission{
				"/v1/videos":     submitThroughMgtvVideosAPI(t, plugin, tc.model, "", tc.body),
				"/v1/tasks/mgtv": submitThroughMgtvTasksEntry(t, plugin, tc.model, tc.body),
			} {
				if tc.reject != "" {
					assert.Contains(t, submission.Rejection, tc.reject, entry)
					assert.NotContains(t, submission.Rejection, "msd-", entry)
					continue
				}
				assert.Equal(t, mgtvSubmission{URL: mgtvSubmitURL, Authorization: "Bearer k", Body: tc.upload, Facts: tc.facts, Action: "text_to_video"}, submission, entry)
			}
		})
	}
}

// The plugin takes only public model names. A channel alias that maps to a
// public model name reaches that model; a MaaS model name, sent by the client
// or reached through a channel mapping, is not available, and the refusal
// does not name it.
func TestMgtvPluginServesOnlyPublicModelNames(t *testing.T) {
	plugin := newMgtvPlugin(t)
	for _, tc := range []struct {
		name      string
		model     string
		mapping   string
		maasModel string
	}{
		{"an alias mapped to a public model name", "seedance-mini", `{"seedance-mini":"seedance-2.0-mini"}`, "msd-2-0-mini-260615"},
		{"a public model name mapped to its MaaS model name", "seedance-2.0-mini", `{"seedance-2.0-mini":"msd-2-0-mini-260615"}`, ""},
		{"a MaaS model name the client sends", "msd-2-5-260628", "", ""},
		{"a model the plugin does not declare", "seedance-3.0", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submission := submitThroughMgtvVideosAPI(t, plugin, tc.model, tc.mapping, `{"model":"`+tc.model+`","prompt":"a cat"}`)
			if tc.maasModel != "" {
				require.Empty(t, submission.Rejection)
				assert.Equal(t, tc.maasModel, submission.Body["model"])
				assert.Equal(t, map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"}, submission.Facts)
				return
			}
			assert.Contains(t, submission.Rejection, "this model is not available on this channel")
			assert.NotContains(t, submission.Rejection, "msd-2-0-mini")
		})
	}
}
