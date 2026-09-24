package mulerouter

// Upstream response shape, shared by the submit (202) and query (200) endpoints:
//
//	{"task_info": {"id", "status", "created_at", "updated_at", "error"},
//	 "images"|"videos"|"audios": ["https://..."]}
type taskResponse struct {
	TaskInfo taskInfo `json:"task_info"`
	Images   []string `json:"images,omitempty"`
	Videos   []string `json:"videos,omitempty"`
	Audios   []string `json:"audios,omitempty"`
}

type taskInfo struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	CreatedAt string     `json:"created_at,omitempty"`
	UpdatedAt string     `json:"updated_at,omitempty"`
	Error     *taskError `json:"error,omitempty"`
}

type taskError struct {
	Code   int    `json:"code,omitempty"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Upstream task states.
const (
	upstreamStatusPending    = "pending"
	upstreamStatusProcessing = "processing"
	upstreamStatusCompleted  = "completed"
	upstreamStatusFailed     = "failed"
)

// artifacts returns the completed task's result URLs and the response key they
// were carried in, so a rebuilt response keeps the vendor's own field name.
func (r *taskResponse) artifacts() (string, []string) {
	switch {
	case len(r.Images) > 0:
		return "images", r.Images
	case len(r.Videos) > 0:
		return "videos", r.Videos
	case len(r.Audios) > 0:
		return "audios", r.Audios
	default:
		return "", nil
	}
}

func (e *taskError) message() string {
	if e == nil {
		return ""
	}
	switch {
	case e.Title != "" && e.Detail != "":
		return e.Title + ": " + e.Detail
	case e.Title != "":
		return e.Title
	default:
		return e.Detail
	}
}
