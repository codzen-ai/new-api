package plugins_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// mgtvReferenceImages is a media list of n reference images, as the client
// writes it; mgtvReferenceImageItems is the same list as MaaS receives it.
func mgtvReferenceImages(n int) string {
	items := make([]string, n)
	for i := range n {
		items[i] = `{"type":"reference_image","url":"https://cdn.example/` + strconv.Itoa(i) + `.png"}`
	}
	return "[" + strings.Join(items, ",") + "]"
}

func mgtvReferenceImageItems(n int) []any {
	items := make([]any, n)
	for i := range n {
		items[i] = map[string]any{"type": "reference_image", "url": "https://cdn.example/" + strconv.Itoa(i) + ".png"}
	}
	return items
}

// A submission with image inputs, a first frame, a first and last frame or
// reference images, sends them to MaaS as image URLs and is an image-to-video
// task billed like text-to-video. Whether the combination of media makes sense
// is MaaS's to judge: it refuses a bad one when the task is created, before
// anything is charged. Both entries must send and bill exactly the same.
func TestMgtvPluginImageInputSubmissions(t *testing.T) {
	plugin := newMgtvPlugin(t)
	facts720p5s := map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"}
	cases := []struct {
		name   string
		model  string
		body   string
		upload map[string]any
		facts  map[string]any
		reject string
	}{
		{
			name:   "input_reference is the first frame",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","input_reference":"https://cdn.example/first.png"}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(5), "input_image_url": "https://cdn.example/first.png", "extra_body": map[string]any{"resolution": "720p"}},
			facts:  facts720p5s,
		},
		{
			name:   "image_url is the first frame, under metadata too",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","seconds":5,"metadata":{"image_url":"https://cdn.example/first.png"}}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(5), "input_image_url": "https://cdn.example/first.png", "extra_body": map[string]any{"resolution": "720p"}},
			facts:  facts720p5s,
		},
		{
			name:   "input_reference and image_url that agree",
			model:  "seedance-2.0-mini",
			body:   `{"model":"seedance-2.0-mini","prompt":"a cat","input_reference":"https://cdn.example/first.png","image_url":"https://cdn.example/first.png"}`,
			upload: map[string]any{"model": "msd-2-0-mini-260615", "prompt": "a cat", "seconds": float64(5), "input_image_url": "https://cdn.example/first.png", "extra_body": map[string]any{"resolution": "720p"}},
			facts:  facts720p5s,
		},
		{
			name:  "media carries a first and a last frame",
			model: "seedance-2.0-fast",
			body:  `{"model":"seedance-2.0-fast","prompt":"a cat","media":[{"type":"first_frame","url":"https://cdn.example/first.png"},{"type":"last_frame","url":"https://cdn.example/last.png"}]}`,
			upload: map[string]any{"model": "msd-2-0-fast-260128", "prompt": "a cat", "seconds": float64(5), "media": []any{
				map[string]any{"type": "first_frame", "url": "https://cdn.example/first.png"},
				map[string]any{"type": "last_frame", "url": "https://cdn.example/last.png"},
			}, "extra_body": map[string]any{"resolution": "720p"}},
			facts: facts720p5s,
		},
		{
			// Reference images alone ask MaaS for multi-image reference, which
			// the caller need not set.
			name:  "reference images set the multi-image reference mode",
			model: "seedance-2.5",
			body:  `{"model":"seedance-2.5","prompt":"a cat","seconds":5,"metadata":{"media":[{"type":"reference_image","url":"https://cdn.example/a.png"},{"type":"reference_image","url":"https://cdn.example/b.png"}]}}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(5), "media": []any{
				map[string]any{"type": "reference_image", "url": "https://cdn.example/a.png"},
				map[string]any{"type": "reference_image", "url": "https://cdn.example/b.png"},
			}, "extra_body": map[string]any{"resolution": "720p", "reference_mode": "multi_ref"}},
			facts: facts720p5s,
		},
		{
			name:   "2.5 takes 30 reference images",
			model:  "seedance-2.5",
			body:   `{"model":"seedance-2.5","prompt":"a cat","seconds":5,"media":` + mgtvReferenceImages(30) + `}`,
			upload: map[string]any{"model": "msd-2-5-260628", "prompt": "a cat", "seconds": float64(5), "media": mgtvReferenceImageItems(30), "extra_body": map[string]any{"resolution": "720p", "reference_mode": "multi_ref"}},
			facts:  facts720p5s,
		},
		{
			name:   "2.0 takes 9 reference images",
			model:  "seedance-2.0-pro",
			body:   `{"model":"seedance-2.0-pro","prompt":"a cat","media":` + mgtvReferenceImages(9) + `}`,
			upload: map[string]any{"model": "msd-2-0-pro-260128", "prompt": "a cat", "seconds": float64(5), "media": mgtvReferenceImageItems(9), "extra_body": map[string]any{"resolution": "720p", "reference_mode": "multi_ref"}},
			facts:  facts720p5s,
		},
		{
			// Mixing a first frame with reference images is MaaS's to refuse; a
			// media list that is not reference images alone sets no mode.
			name:  "media that mixes roles is forwarded without a reference mode",
			model: "seedance-2.0-mini",
			body:  `{"model":"seedance-2.0-mini","prompt":"a cat","media":[{"type":"first_frame","url":"https://cdn.example/first.png"},{"type":"reference_image","url":"https://cdn.example/a.png"}]}`,
			upload: map[string]any{"model": "msd-2-0-mini-260615", "prompt": "a cat", "seconds": float64(5), "media": []any{
				map[string]any{"type": "first_frame", "url": "https://cdn.example/first.png"},
				map[string]any{"type": "reference_image", "url": "https://cdn.example/a.png"},
			}, "extra_body": map[string]any{"resolution": "720p"}},
			facts: facts720p5s,
		},
		{name: "2.5 above 30 reference images", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":` + mgtvReferenceImages(31) + `}`, reject: "media takes at most 30 reference images on this model, got 31"},
		{name: "2.0 above 9 reference images", model: "seedance-2.0-fast", body: `{"model":"seedance-2.0-fast","prompt":"a cat","media":` + mgtvReferenceImages(10) + `}`, reject: "media takes at most 9 reference images on this model, got 10"},
		{name: "a media item with an asset_id", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":[{"type":"reference_image","url":"https://cdn.example/a.png","asset_id":"a1"}]}`, reject: "media[0] takes only type and url; remove asset_id"},
		{name: "a media item that is only an asset_id", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":[{"type":"reference_image","asset_id":"a1"}]}`, reject: "media[0] takes only type and url; remove asset_id"},
		{name: "a media url that is not http(s)", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":[{"type":"first_frame","url":"https://cdn.example/a.png"},{"type":"last_frame","url":"ftp://cdn.example/b.png"}]}`, reject: "media[1].url must be an http(s) URL"},
		{name: "a media item without a url", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":[{"type":"first_frame"}]}`, reject: "media[0].url must be an http(s) URL"},
		{name: "an audio media role", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":[{"type":"reference_audio","url":"https://cdn.example/a.mp3"}]}`, reject: `media[0].type must be one of first_frame, last_frame, reference_image, got "reference_audio"`},
		{name: "a reference video is not taken yet", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":[{"type":"reference_video","url":"https://cdn.example/a.mp4"}]}`, reject: `media[0].type must be one of first_frame, last_frame, reference_image, got "reference_video"`},
		{name: "media that is not an array", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","media":{"type":"first_frame","url":"https://cdn.example/a.png"}}`, reject: "media must be an array of {type, url} items"},
		{name: "input_reference and image_url that disagree", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","input_reference":"https://cdn.example/a.png","image_url":"https://cdn.example/b.png"}`, reject: "image_url and input_reference give different values"},
		{name: "a first frame that is not an http(s) URL", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","input_reference":"data:image/png;base64,AAAA"}`, reject: "input_reference must be an http(s) URL"},
		{name: "a first frame that is not a string", model: "seedance-2.5", body: `{"model":"seedance-2.5","prompt":"a cat","image_url":{"url":"https://cdn.example/a.png"}}`, reject: "image_url must be an http(s) URL"},
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
				assert.Equal(t, mgtvSubmission{URL: mgtvSubmitURL, Authorization: "Bearer k", Body: tc.upload, Facts: tc.facts, Action: "image_to_video"}, submission, entry)
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

// Responses MaaS returned on GET /v1/videos/{id} for real tasks (signatures
// redacted): a 5-second 720p seedance-2.5 video billed 108,900 tokens, and a
// task that failed upstream and billed nothing.
const (
	mgtvResultURL        = "https://mgyun-maas-1382816285.cos.ap-beijing.myqcloud.com/82/2610/1615/0759/i1ZoIhp1SvZHF23h/612663996059676672.mp4?sign=q-sign-algorithm%3Dsha1%26q-signature%3Dredacted"
	mgtvCompletedVideo   = `{"id":"8605ab6711f30f8e34e5fa441813912a","object":"video","model":"msd-2-5-260628","status":"completed","progress":100,"created_at":1792134304,"updated_at":1792134488,"url":"` + mgtvResultURL + `","usage":{"completion_tokens":108900,"output_tokens":108900,"total_tokens":108900,"video_seconds":5},"provider_task_id":"cgt-20261016150508-dp5k8","provider_usage":{"prompt_tokens":0,"completion_tokens":108900,"total_tokens":108900,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"tool_usage":{"web_search":0}},"provider_result_url":"https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/doubao-seedance-2-5/2122459029/Video/cgt-20261016150508-dp5k8.mp4?X-Tos-Signature=redacted"}`
	mgtvFailedVideo      = `{"id":"22289b2c9dd8af41ce8b5061ba0f6dcf","object":"video","model":"msd-2-5-260628","status":"failed","progress":100,"created_at":1792138436,"updated_at":1792138599,"usage":{"video_seconds":5},"error":{"code":"upstream_invoke_failed","message":"上游服务处理失败，请稍后重试。"},"provider_task_id":"cgt-20261016161358-12olq"}`
	mgtvUpstreamFailText = "上游服务处理失败，请稍后重试。"
)

// mgtvTask is a task the plugin submitted for seedance-2.5.
func mgtvTask(status model.TaskStatus, failReason, data string) *model.Task {
	task := &model.Task{
		TaskID:      "task_public",
		Platform:    constant.TaskPlatform(mgtvPluginKey),
		Action:      "text_to_video",
		Status:      status,
		FailReason:  failReason,
		CreatedAt:   1792134304,
		UpdatedAt:   1792134488,
		Properties:  model.Properties{OriginModelName: "seedance-2.5", UpstreamModelName: "seedance-2.5"},
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "8605ab6711f30f8e34e5fa441813912a"},
		Data:        []byte(data),
	}
	if status == model.TaskStatusSuccess {
		task.FinishTime = task.UpdatedAt
	}
	return task
}

// The plugin reads MaaS's submit and poll responses: the task id, the
// gateway status, the failure reason, the result URL and the tokens the task
// settles on.
func TestMgtvPluginReadsTasks(t *testing.T) {
	plugin := newMgtvPlugin(t)
	adaptor := taskplugin.New(plugin)
	info := mgtvPluginChannel()
	info.OriginModelName, info.UpstreamModelName = "seedance-2.5", "seedance-2.5"
	adaptor.Init(info)

	t.Run("submit response", func(t *testing.T) {
		submit := func(body string) (string, *taskdto.TaskError) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			parsed, taskErr := adaptor.ParseResponse(c, resp, info)
			if taskErr != nil {
				return "", taskErr
			}
			return parsed.UpstreamTaskID, nil
		}
		id, taskErr := submit(`{"id":"8605ab6711f30f8e34e5fa441813912a","object":"video","model":"msd-2-5-260628","status":"queued","progress":0,"created_at":1792134304}`)
		require.Nil(t, taskErr)
		assert.Equal(t, "8605ab6711f30f8e34e5fa441813912a", id)

		_, taskErr = submit(`{"object":"video","status":"queued"}`)
		require.NotNil(t, taskErr)
	})

	t.Run("query request", func(t *testing.T) {
		var requested *http.Request
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requested = r
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"8605ab6711f30f8e34e5fa441813912a","status":"in_progress"}`)
		}))
		defer server.Close()
		resp, err := adaptor.FetchTask(server.URL, "k", mgtvTask(model.TaskStatusInProgress, "", ""), "")
		require.NoError(t, err)
		resp.Body.Close()
		require.NotNil(t, requested)
		assert.Equal(t, http.MethodGet, requested.Method)
		assert.Equal(t, "/v1/videos/8605ab6711f30f8e34e5fa441813912a", requested.URL.Path)
		assert.Equal(t, "Bearer k", requested.Header.Get("Authorization"))
	})

	t.Run("poll results", func(t *testing.T) {
		task := mgtvTask(model.TaskStatusInProgress, "", "")
		cases := []struct {
			name   string
			body   string
			status model.TaskStatus
			url    string
			reason string
			// facts are the completion facts that replace the reservation; a
			// task without them keeps the reserved tokens.
			facts map[string]any
		}{
			{"queued", `{"id":"up-1","status":"queued","progress":0}`, model.TaskStatusSubmitted, "", "", nil},
			{"pending", `{"id":"up-1","status":"pending"}`, model.TaskStatusSubmitted, "", "", nil},
			{"in_progress", `{"id":"up-1","status":"in_progress","progress":40}`, model.TaskStatusInProgress, "", "", nil},
			{"processing", `{"id":"up-1","status":"processing"}`, model.TaskStatusInProgress, "", "", nil},
			{"running", `{"id":"up-1","status":"running"}`, model.TaskStatusInProgress, "", "", nil},
			{"completed settles on the tokens MaaS reports", mgtvCompletedVideo, model.TaskStatusSuccess, mgtvResultURL, "", map[string]any{"tokens": float64(108900)}},
			{"succeeded", `{"id":"up-1","status":"succeeded","url":"https://cdn.example/v.mp4","usage":{"completion_tokens":90000,"total_tokens":95000}}`, model.TaskStatusSuccess, "https://cdn.example/v.mp4", "", map[string]any{"tokens": float64(90000)}},
			{"total_tokens without completion_tokens", `{"id":"up-1","status":"completed","url":"https://cdn.example/v.mp4","usage":{"total_tokens":95000}}`, model.TaskStatusSuccess, "https://cdn.example/v.mp4", "", map[string]any{"tokens": float64(95000)}},
			{"zero completion_tokens falls back to total_tokens", `{"id":"up-1","status":"completed","url":"https://cdn.example/v.mp4","usage":{"completion_tokens":0,"total_tokens":95000}}`, model.TaskStatusSuccess, "https://cdn.example/v.mp4", "", map[string]any{"tokens": float64(95000)}},
			{"no usage keeps the reservation", `{"id":"up-1","status":"completed","url":"https://cdn.example/v.mp4"}`, model.TaskStatusSuccess, "https://cdn.example/v.mp4", "", nil},
			{"invalid usage keeps the reservation", `{"id":"up-1","status":"completed","url":"https://cdn.example/v.mp4","usage":{"completion_tokens":-5,"total_tokens":"lots"}}`, model.TaskStatusSuccess, "https://cdn.example/v.mp4", "", nil},
			// A success with nothing to deliver fails, so the host refunds it.
			{"completed without a url", `{"id":"up-1","status":"completed","usage":{"total_tokens":95000}}`, model.TaskStatusFailure, "", "the task completed without producing a video", nil},
			// The reason is MaaS's own message, passed on as it is.
			{"failed carries the upstream message", mgtvFailedVideo, model.TaskStatusFailure, "", mgtvUpstreamFailText, nil},
			{"failed without a message", `{"id":"up-1","status":"failed"}`, model.TaskStatusFailure, "", "task failed", nil},
			{"cancelled", `{"id":"up-1","status":"cancelled"}`, model.TaskStatusFailure, "", "task cancelled", nil},
			{"expired", `{"id":"up-1","status":"expired","error":{"code":"expired","message":"task expired"}}`, model.TaskStatusFailure, "", "task expired", nil},
			// An unknown status is a failed poll, never progress.
			{"unknown status", `{"id":"up-1","status":"paused"}`, "UNKNOWN", "", "unknown task status: paused", nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, []byte(tc.body))
				require.NoError(t, err)
				assert.Equal(t, string(tc.status), result.Status)
				assert.Equal(t, tc.url, result.Url)
				assert.Equal(t, tc.reason, result.Reason)
				assert.Equal(t, tc.facts, result.UsageFacts)
			})
		}
	})
}

// GET /v1/videos/{task_id} renders a task as an OpenAI video object without
// anything that names MaaS or lets the caller bypass the gateway: no result
// URL, usage, upstream task id, upstream usage or upstream result URL. The
// video is downloaded from GET /v1/videos/{task_id}/content, which the
// gateway proxies from the stored result URL without the channel key.
func TestMgtvPluginRendersOpenAIVideo(t *testing.T) {
	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(mgtvPluginKey))
	require.NotNil(t, adaptor)
	adaptor.Init(mgtvPluginChannel())
	converter, ok := adaptor.(relaychannel.OpenAIVideoConverter)
	require.True(t, ok)

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
			data:   `{"id":"8605ab6711f30f8e34e5fa441813912a","object":"video","model":"msd-2-5-260628","status":"queued","progress":0,"created_at":1792134304}`,
			want:   `{"id":"task_public","object":"video","model":"seedance-2.5","status":"queued","progress":0,"created_at":1792134304}`,
		},
		{
			name:     "completed",
			status:   model.TaskStatusSuccess,
			progress: "100%",
			data:     mgtvCompletedVideo,
			want:     `{"id":"task_public","object":"video","model":"seedance-2.5","status":"completed","progress":100,"created_at":1792134304,"completed_at":1792134488}`,
		},
		{
			name:       "failed carries the failure reason",
			status:     model.TaskStatusFailure,
			progress:   "100%",
			failReason: mgtvUpstreamFailText,
			data:       mgtvFailedVideo,
			want:       `{"id":"task_public","object":"video","model":"seedance-2.5","status":"failed","progress":100,"created_at":1792134304,"error":{"message":"` + mgtvUpstreamFailText + `","code":"task_failed"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := mgtvTask(tc.status, tc.failReason, tc.data)
			task.Progress = tc.progress
			rendered, err := converter.ConvertToOpenAIVideo(task)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(rendered))
		})
	}

	t.Run("content is the stored result URL, fetched without credentials", func(t *testing.T) {
		artifacts, ok := adaptor.(relaychannel.TaskArtifactProvider)
		require.True(t, ok)
		provider, ok := adaptor.(relaychannel.TaskContentRequestProvider)
		require.True(t, ok)

		done := mgtvTask(model.TaskStatusSuccess, "", mgtvCompletedVideo)
		listed, err := artifacts.ListArtifacts(done)
		require.NoError(t, err)
		assert.Equal(t, []relaychannel.TaskArtifact{{Key: "video", Type: "video"}}, listed)
		descriptor, err := provider.BuildContentRequest(t.Context(), done, "video", relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
		require.NoError(t, err)
		assert.Equal(t, mgtvResultURL, descriptor.URL)
		assert.Equal(t, http.MethodGet, descriptor.Method)
		assert.True(t, descriptor.Credentialless)
		assert.Empty(t, descriptor.Headers["Authorization"])

		_, err = provider.BuildContentRequest(t.Context(), done, "thumbnail", relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
		require.ErrorContains(t, err, "artifact_not_found")

		for _, task := range []*model.Task{
			mgtvTask(model.TaskStatusInProgress, "", `{"id":"up-1","status":"in_progress"}`),
			mgtvTask(model.TaskStatusFailure, mgtvUpstreamFailText, mgtvFailedVideo),
		} {
			listed, err = artifacts.ListArtifacts(task)
			require.NoError(t, err)
			assert.Empty(t, listed, string(task.Status))
		}
	})
}
