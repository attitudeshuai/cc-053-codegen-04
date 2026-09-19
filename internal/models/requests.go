package models

type CreateWordlistRequest struct {
	Name    string  `json:"name" binding:"required"`
	Entries []Entry `json:"entries" binding:"required"`
}

type CreateSpeakerRequest struct {
	CodeName         string `json:"code_name" binding:"required"`
	BirthYear        int    `json:"birth_year" binding:"required"`
	Gender           string `json:"gender" binding:"required,oneof=male female other"`
	DialectPointCode string `json:"dialect_point_code" binding:"required"`
	Occupation       string `json:"occupation"`
	YearsAway        int    `json:"years_away"`
	ContactRef       string `json:"contact_ref"`
}

type CreateTaskRequest struct {
	WordlistID int64  `json:"wordlist_id" binding:"required"`
	SpeakerID  int64  `json:"speaker_id" binding:"required"`
	Kind       string `json:"kind" binding:"required,oneof=record annotate"`
	Assignee   string `json:"assignee"`
}

type UploadURLRequest struct {
	Filename string `json:"filename" binding:"required"`
	TaskID   int64  `json:"task_id" binding:"required"`
}

type UploadURLResponse struct {
	URL        string `json:"url"`
	ObjectKey  string `json:"object_key"`
	ExpiresIn  int    `json:"expires_in"`
}

type CreateRecordingRequest struct {
	TaskID     int64  `json:"task_id" binding:"required"`
	ObjectKey  string `json:"object_key" binding:"required"`
	DurationMs int    `json:"duration_ms"`
	SampleRate int    `json:"sample_rate"`
	PeakDB     float64 `json:"peak_db"`
	Device     string `json:"device"`
	RecordedAt string `json:"recorded_at"`
}

type AnnotationRequest struct {
	IPA       string `json:"ipa" binding:"required"`
	Tone      string `json:"tone"`
	Note      string `json:"note"`
	Version   int    `json:"version" binding:"required"`
}

type ArbitrateRequest struct {
	WinnerAnnotationID int64  `json:"winner_annotation_id" binding:"required"`
	Arbiter            string `json:"arbiter" binding:"required"`
	Reason             string `json:"reason"`
}

type CreateExportRequest struct {
	WordlistID int64 `json:"wordlist_id"`
	TaskID     int64 `json:"task_id"`
	SpeakerID  int64 `json:"speaker_id"`
	Status     string `json:"status"`
}

// CreateListeningExperimentRequest 建一批听辨实验：
// 按调查点从词表挑条目（显式 entry_ids 或按 pick_count 抽取），分派给 listeners。
type CreateListeningExperimentRequest struct {
	Name             string   `json:"name" binding:"required"`
	DialectPointCode string   `json:"dialect_point_code" binding:"required"`
	WordlistID       int64    `json:"wordlist_id" binding:"required"`
	Listeners        []string `json:"listeners" binding:"required,min=1"`
	EntryIDs         []int64  `json:"entry_ids"`  // 显式挑条目；为空时按 pick_count 从词表抽
	PickCount        int      `json:"pick_count"` // 0 且 entry_ids 为空 = 用词表全部条目
	Seed             int64    `json:"seed"`       // 0 = 服务端随机生成
}

// ListeningAnswerInput 一条作答；提交按 trial_id 定位试次。
type ListeningAnswerInput struct {
	TrialID int64  `json:"trial_id" binding:"required"`
	Choice  string `json:"choice" binding:"required"`
	Note    string `json:"note"`
}

// SubmitListeningResponsesRequest 听辨人批量交作答；重复提交并掉，只留第一次。
type SubmitListeningResponsesRequest struct {
	Answers []ListeningAnswerInput `json:"answers" binding:"required,min=1"`
}

// VoidListeningTrialsRequest 作废一批进行中的试次。
type VoidListeningTrialsRequest struct {
	TrialIDs []int64 `json:"trial_ids" binding:"required,min=1"`
}

// RedispatchListeningRequest 重排补位：把已作废且未补发的条目重新编进新一轮；
// 可只补某个听辨人，也可额外指定补位条目。
type RedispatchListeningRequest struct {
	Listener string  `json:"listener"`   // 空 = 所有有缺口的听辨人
	EntryIDs []int64 `json:"entry_ids"`  // 额外补位条目；空 = 只重排作废缺口
}

type ListeningTrialQuery struct {
	Listener string `form:"listener"`
	Status   string `form:"status"`
	Pagination
}

type SegmentQuery struct {
	TaskID int64  `form:"task_id"`
	Status string `form:"status"`
	Pagination
}

type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type HealthResponse struct {
	Status    string `json:"status"`
	Database  string `json:"database"`
	Redis     string `json:"redis"`
	MinIO     string `json:"minio"`
}