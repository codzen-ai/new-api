package mulerouter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePath(t *testing.T) {
	testCases := []struct {
		name   string
		vendor string
		rest   string
		want   PathRequest
		ok     bool
	}{
		{
			name: "submit", vendor: "carrothub", rest: "/wan2.2-i2v-spicy/generation", ok: true,
			want: PathRequest{Vendor: "carrothub", Model: "wan2.2-i2v-spicy", Action: "generation"},
		},
		{
			name: "query", vendor: "carrothub", rest: "/z-image-spicy/generation/8a1b-uuid", ok: true,
			want: PathRequest{Vendor: "carrothub", Model: "z-image-spicy", Action: "generation", TaskID: "8a1b-uuid"},
		},
		{
			name: "multi-word action", vendor: "klingai", rest: "/kling-v3/image-to-video", ok: true,
			want: PathRequest{Vendor: "klingai", Model: "kling-v3", Action: "image-to-video"},
		},
		{name: "missing action", vendor: "carrothub", rest: "/z-image-spicy"},
		{name: "too many segments", vendor: "carrothub", rest: "/z-image-spicy/generation/id/extra"},
		{name: "empty vendor", vendor: "", rest: "/z-image-spicy/generation"},
		{name: "empty tail", vendor: "carrothub", rest: "/"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, ok := ParsePath(tc.vendor, tc.rest)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.want, parsed)
			}
		})
	}
}

func TestParsePathModelNameMatchesRouteIdentity(t *testing.T) {
	parsed, ok := ParsePath("carrothub", "/wan2.2-i2v-spicy/generation")
	require.True(t, ok)
	assert.Equal(t, "carrothub/wan2.2-i2v-spicy/generation", parsed.ModelName())
}

func TestParseTaskResult(t *testing.T) {
	adaptor := &TaskAdaptor{}

	testCases := []struct {
		name       string
		body       string
		wantStatus model.TaskStatus
		wantURL    string
		wantReason string
	}{
		{
			name:       "pending",
			body:       `{"task_info":{"id":"t1","status":"pending"}}`,
			wantStatus: model.TaskStatusSubmitted,
		},
		{
			name:       "processing",
			body:       `{"task_info":{"id":"t1","status":"processing"}}`,
			wantStatus: model.TaskStatusInProgress,
		},
		{
			name:       "completed with images",
			body:       `{"task_info":{"id":"t1","status":"completed"},"images":["https://cdn/a.png"]}`,
			wantStatus: model.TaskStatusSuccess,
			wantURL:    "https://cdn/a.png",
		},
		{
			name:       "completed with videos",
			body:       `{"task_info":{"id":"t1","status":"completed"},"videos":["https://cdn/a.mp4"]}`,
			wantStatus: model.TaskStatusSuccess,
			wantURL:    "https://cdn/a.mp4",
		},
		{
			name:       "failed carries the upstream error",
			body:       `{"task_info":{"id":"t1","status":"failed","error":{"code":400,"title":"bad_prompt","detail":"blocked"}}}`,
			wantStatus: model.TaskStatusFailure,
			wantReason: "bad_prompt: blocked",
		},
		{
			name:       "failed without an error object",
			body:       `{"task_info":{"id":"t1","status":"failed"}}`,
			wantStatus: model.TaskStatusFailure,
			wantReason: "task failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := adaptor.ParseTaskResult([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, model.TaskStatus(result.Status))
			assert.Equal(t, tc.wantURL, result.Url)
			assert.Equal(t, tc.wantReason, result.Reason)
		})
	}

	// An unrecognised state must not be silently mapped onto a terminal one:
	// mapping it to success would settle a task that never produced anything.
	_, err := adaptor.ParseTaskResult([]byte(`{"task_info":{"id":"t1","status":"paused"}}`))
	require.Error(t, err)
}

func TestBuildFetchResponseReplacesUpstreamTaskID(t *testing.T) {
	task := &model.Task{
		TaskID:    "task_local",
		Status:    model.TaskStatusSuccess,
		CreatedAt: 1700000000,
		UpdatedAt: 1700000060,
		Data:      json.RawMessage(`{"task_info":{"id":"upstream-uuid","status":"completed"},"images":["https://cdn/a.png"]}`),
	}

	body, err := BuildFetchResponse(task)
	require.NoError(t, err)

	var parsed taskResponse
	require.NoError(t, common.Unmarshal(body, &parsed))
	assert.Equal(t, "task_local", parsed.TaskInfo.ID, "the upstream task id must never leak")
	assert.Equal(t, upstreamStatusCompleted, parsed.TaskInfo.Status)
	assert.Equal(t, []string{"https://cdn/a.png"}, parsed.Images)
	assert.Equal(t, "2023-11-14T22:13:20Z", parsed.TaskInfo.CreatedAt)
	assert.Equal(t, "2023-11-14T22:14:20Z", parsed.TaskInfo.UpdatedAt)
}

func TestBuildFetchResponseStatusMapping(t *testing.T) {
	testCases := []struct {
		name   string
		status model.TaskStatus
		want   string
	}{
		{name: "not started", status: model.TaskStatusNotStart, want: upstreamStatusPending},
		{name: "submitted", status: model.TaskStatusSubmitted, want: upstreamStatusPending},
		{name: "in progress", status: model.TaskStatusInProgress, want: upstreamStatusProcessing},
		{name: "success", status: model.TaskStatusSuccess, want: upstreamStatusCompleted},
		{name: "failure", status: model.TaskStatusFailure, want: upstreamStatusFailed},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := BuildFetchResponse(&model.Task{TaskID: "task_local", Status: tc.status})
			require.NoError(t, err)

			var parsed taskResponse
			require.NoError(t, common.Unmarshal(body, &parsed))
			assert.Equal(t, tc.want, parsed.TaskInfo.Status)
		})
	}
}

// A task whose stored payload still holds artifacts from a stale poll must not
// advertise them while it is back in a non-terminal state.
func TestBuildFetchResponseHidesArtifactsUntilCompleted(t *testing.T) {
	task := &model.Task{
		TaskID: "task_local",
		Status: model.TaskStatusInProgress,
		Data:   json.RawMessage(`{"task_info":{"id":"upstream","status":"completed"},"images":["https://cdn/a.png"]}`),
	}

	body, err := BuildFetchResponse(task)
	require.NoError(t, err)

	var parsed taskResponse
	require.NoError(t, common.Unmarshal(body, &parsed))
	assert.Empty(t, parsed.Images)
}

func TestBuildFetchResponseSynthesisesFailureError(t *testing.T) {
	task := &model.Task{TaskID: "task_local", Status: model.TaskStatusFailure, FailReason: "任务超时（1440分钟）"}

	body, err := BuildFetchResponse(task)
	require.NoError(t, err)

	var parsed taskResponse
	require.NoError(t, common.Unmarshal(body, &parsed))
	require.NotNil(t, parsed.TaskInfo.Error)
	assert.Equal(t, "任务超时（1440分钟）", parsed.TaskInfo.Error.Detail)
}

func contextWithBody(t *testing.T, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

// Vendor fields arrive two ways — under metadata from the vendor-compatible
// routes, at the top level from callers on the unified task route — and both
// have to reach the vendor. A top-level field that is silently dropped hands
// the caller a default-parameter result they did not ask for.
func TestMergeUpstreamParams(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "vendor fields at the top level",
			body: `{"model":"m","prompt":"a cat","image":"https://cdn/in.png",
			        "resolution":"720p","duration":8,"seed":0,"prompt_extend":false}`,
			want: map[string]any{
				"prompt": "a cat", "image": "https://cdn/in.png",
				"resolution": "720p", "duration": float64(8),
				"seed": float64(0), "prompt_extend": false,
			},
		},
		{
			name: "vendor fields under metadata",
			body: `{"model":"m","prompt":"a cat",
			        "metadata":{"resolution":"720p","duration":8,"seed":0,"prompt_extend":false}}`,
			want: map[string]any{
				"prompt": "a cat", "resolution": "720p", "duration": float64(8),
				"seed": float64(0), "prompt_extend": false,
			},
		},
		{
			name: "metadata wins over the top level",
			body: `{"model":"m","prompt":"top","metadata":{"prompt":"meta"}}`,
			want: map[string]any{"prompt": "meta"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params, err := mergeUpstreamParams(contextWithBody(t, tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, params)
			// new-api's own routing fields must never reach the vendor.
			assert.NotContains(t, params, "model")
			assert.NotContains(t, params, "metadata")
		})
	}
}

// Explicit zero values are meaningful upstream: seed 0 means "random" and
// prompt_extend false disables a billable rewrite, so neither may be dropped.
func TestMergeUpstreamParamsPreservesExplicitZeroValues(t *testing.T) {
	params, err := mergeUpstreamParams(contextWithBody(t,
		`{"model":"m","prompt":"a cat","seed":0,"prompt_extend":false}`))
	require.NoError(t, err)

	encoded, err := common.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"seed":0`)
	assert.Contains(t, string(encoded), `"prompt_extend":false`)
}
