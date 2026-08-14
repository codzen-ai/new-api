package mulerouter

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

const ChannelName = "mulerouter"

// controlKeys are unified-request fields that must never reach the upstream
// body: they configure new-api's own routing, not the vendor endpoint.
var controlKeys = []string{"model", "metadata"}

// TaskAdaptor relays MuleRouter's asynchronous vendor endpoints. Which vendors
// and models are reachable is channel configuration, not code — see
// dto.MuleRouterConfig.
type TaskAdaptor struct {
	taskcommon.BaseBilling

	baseURL string
	apiKey  string

	route  *dto.MuleRouterRoute
	params map[string]any
	ratios map[string]float64
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

// GetModelList is empty by design: the reachable models are the channel's
// configured routes, which this adaptor instance cannot see at registry time.
func (a *TaskAdaptor) GetModelList() []string {
	return []string{}
}

// ValidateRequestAndSetAction resolves the route, bounds every declared billing
// field and builds the upstream body. It runs before price calculation and
// pre-consumption, which is the only point where a request can still be
// refused without having charged for it.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	config := info.ChannelOtherSettings.MuleRouter
	if config == nil {
		return service.TaskErrorWrapperLocal(errors.New("channel is missing its mulerouter route table"), "mulerouter_not_configured", http.StatusInternalServerError)
	}

	// Resolve the route against the mapped name so a channel model mapping can
	// give callers a short, vendor-neutral model name. Mapping is administrator
	// configuration, the same as it is on every other channel; the request-level
	// billing bounds below are what protect against user-controlled input.
	if err := helper.ModelMappedHelper(c, info, nil); err != nil {
		return service.TaskErrorWrapperLocal(err, "model_mapping_failed", http.StatusBadRequest)
	}
	route, ok := config.FindRouteByModelName(info.UpstreamModelName)
	if !ok {
		return service.TaskErrorWrapperLocal(fmt.Errorf("model %s is not configured on this channel", info.OriginModelName), "model_not_found", http.StatusNotFound)
	}
	a.route = route
	info.Action = route.ResolvedAction()

	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, info.Action); taskErr != nil {
		return taskErr
	}

	params, err := mergeUpstreamParams(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	// Billing bounds are checked against the merged body that will actually be
	// sent upstream, so neither metadata nor a top-level field can smuggle a
	// multiplier past them.
	a.params = params
	ratios, err := route.EvaluateBilling(params)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	a.ratios = ratios
	return nil
}

// mergeUpstreamParams builds the upstream body from the request as the caller
// wrote it, minus new-api's own routing fields.
//
// Vendor fields are accepted both at the top level and under metadata, because
// both spellings are in use: the vendor-compatible routes put the original body
// under metadata, while callers on the unified task route naturally write the
// vendor's own fields at the top level. Reading the raw body rather than the
// parsed TaskSubmitReq is what makes the latter work at all — that struct only
// carries new-api's unified vocabulary, so any field it does not name (width,
// resolution, prompt_extend, ...) would otherwise be dropped before the adaptor
// ever saw it, and the caller would silently get a default-parameter result.
//
// Metadata wins on conflict: it is the explicit, unambiguous channel.
func mergeUpstreamParams(c *gin.Context) (map[string]any, error) {
	var body map[string]any
	if err := common.UnmarshalBodyReusable(c, &body); err != nil {
		return nil, err
	}

	params := make(map[string]any, len(body))
	for key, value := range body {
		params[key] = value
	}
	if metadata, ok := body["metadata"].(map[string]any); ok {
		for key, value := range metadata {
			params[key] = value
		}
	}
	for _, key := range controlKeys {
		delete(params, key)
	}
	return params, nil
}

func (a *TaskAdaptor) EstimateBilling(_ *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	return a.ratios
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if a.route == nil {
		return "", errors.New("mulerouter route not resolved")
	}
	path, ok := dto.MuleRouterUpstreamPath(a.route.ModelName())
	if !ok {
		return "", fmt.Errorf("invalid mulerouter model name %s", a.route.ModelName())
	}
	return a.baseURL + path, nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(_ *gin.Context, _ *relaycommon.RelayInfo) (io.Reader, error) {
	if a.params == nil {
		return nil, errors.New("mulerouter request not validated")
	}
	data, err := common.Marshal(a.params)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *taskdto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}

	var parsed taskResponse
	if err := common.Unmarshal(responseBody, &parsed); err != nil {
		return "", nil, service.TaskErrorWrapper(errors.Wrap(err, string(responseBody)), "unmarshal_response_failed", http.StatusInternalServerError)
	}
	if parsed.TaskInfo.ID == "" {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("upstream returned no task id: %s", responseBody), "invalid_upstream_response", http.StatusInternalServerError)
	}
	if parsed.TaskInfo.Status == upstreamStatusFailed {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("task failed: %s", parsed.TaskInfo.Error.message()), "task_failed", http.StatusBadRequest)
	}

	if c.GetBool(ContextKeyNativeRoute) {
		submitted := taskResponse{TaskInfo: taskInfo{
			ID:        info.PublicTaskID,
			Status:    upstreamStatusPending,
			CreatedAt: formatTimestamp(time.Now().Unix()),
			UpdatedAt: formatTimestamp(time.Now().Unix()),
		}}
		c.JSON(http.StatusAccepted, submitted)
	} else {
		video := dto.NewOpenAIVideo()
		video.ID = info.PublicTaskID
		video.TaskID = info.PublicTaskID
		video.CreatedAt = time.Now().Unix()
		video.Model = info.OriginModelName
		c.JSON(http.StatusOK, video)
	}
	return parsed.TaskInfo.ID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	modelName, _ := body["model"].(string)
	if taskID == "" {
		return nil, errors.New("invalid task_id")
	}
	path, ok := dto.MuleRouterUpstreamPath(modelName)
	if !ok {
		return nil, fmt.Errorf("cannot rebuild query url from model name %q", modelName)
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s%s/%s", baseUrl, path, taskID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var parsed taskResponse
	if err := common.Unmarshal(respBody, &parsed); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal response body")
	}

	result := &relaycommon.TaskInfo{TaskID: parsed.TaskInfo.ID}
	switch parsed.TaskInfo.Status {
	case upstreamStatusPending:
		result.Status = model.TaskStatusSubmitted
	case upstreamStatusProcessing:
		result.Status = model.TaskStatusInProgress
	case upstreamStatusCompleted:
		result.Status = model.TaskStatusSuccess
		if _, urls := parsed.artifacts(); len(urls) > 0 {
			result.Url = urls[0]
		}
	case upstreamStatusFailed:
		result.Status = model.TaskStatusFailure
		result.Reason = parsed.TaskInfo.Error.message()
		if result.Reason == "" {
			result.Reason = "task failed"
		}
	default:
		return nil, fmt.Errorf("unknown task status: %s", parsed.TaskInfo.Status)
	}
	return result, nil
}

// ConvertToOpenAIVideo renders a task for the OpenAI video API. Only video
// routes can be expressed in that shape.
func (a *TaskAdaptor) ConvertToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	var parsed taskResponse
	if err := common.Unmarshal(originTask.Data, &parsed); err != nil {
		return nil, errors.Wrap(err, "unmarshal mulerouter task data failed")
	}
	key, urls := parsed.artifacts()
	if key != "" && key != "videos" {
		return nil, fmt.Errorf("model %s does not produce videos", originTask.Properties.OriginModelName)
	}

	video := dto.NewOpenAIVideo()
	video.ID = originTask.TaskID
	video.Status = originTask.Status.ToVideoStatus()
	video.SetProgressStr(originTask.Progress)
	video.CreatedAt = originTask.CreatedAt
	video.CompletedAt = originTask.UpdatedAt
	video.Model = originTask.Properties.OriginModelName
	if len(urls) > 0 {
		video.SetMetadata("url", urls[0])
	}
	if originTask.Status == model.TaskStatusFailure {
		video.Error = &dto.OpenAIVideoError{Message: originTask.FailReason, Code: "task_failed"}
	}
	return common.Marshal(video)
}

// BuildFetchResponse renders a stored task in MuleRouter's own response shape.
// The upstream task id is replaced by our public one and the artifacts keep the
// vendor's field name (images / videos / audios).
func BuildFetchResponse(task *model.Task) ([]byte, error) {
	parsed := taskResponse{}
	if len(task.Data) > 0 {
		if err := common.Unmarshal(task.Data, &parsed); err != nil {
			return nil, errors.Wrap(err, "unmarshal mulerouter task data failed")
		}
	}

	parsed.TaskInfo.ID = task.TaskID
	parsed.TaskInfo.Status = upstreamStatusOf(task.Status)
	parsed.TaskInfo.CreatedAt = formatTimestamp(task.CreatedAt)
	parsed.TaskInfo.UpdatedAt = formatTimestamp(task.UpdatedAt)
	if parsed.TaskInfo.Status == upstreamStatusFailed && parsed.TaskInfo.Error == nil {
		parsed.TaskInfo.Error = &taskError{Code: http.StatusBadRequest, Title: "task_failed", Detail: task.FailReason}
	}
	if parsed.TaskInfo.Status != upstreamStatusCompleted {
		parsed.Images, parsed.Videos, parsed.Audios = nil, nil, nil
	}
	return common.Marshal(parsed)
}

func upstreamStatusOf(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusSuccess:
		return upstreamStatusCompleted
	case model.TaskStatusFailure:
		return upstreamStatusFailed
	case model.TaskStatusInProgress:
		return upstreamStatusProcessing
	default:
		return upstreamStatusPending
	}
}

func formatTimestamp(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}
