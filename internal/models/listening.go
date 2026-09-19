package models

import "time"

// 听辨实验状态
// active    编排完成、可发可收
// completed 已收尾（数据冻结，不再接受作答）
// archived  归档只读
const (
	ListeningStatusActive    = "active"
	ListeningStatusCompleted = "completed"
	ListeningStatusArchived  = "archived"
)

// 试次 / 分发状态
const (
	TrialStatusActive    = "active"
	TrialStatusVoided    = "voided"
	AssignStatusPending  = "pending"
	AssignStatusAnswered = "answered"
	AssignStatusVoided   = "voided"
)

// ListeningExperiment 听辨实验：按调查点挑出若干条目，打乱编成一批试次分发给听辨人。
type ListeningExperiment struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	DialectPointCode string  `json:"dialect_point_code"`
	WordlistID       int64   `json:"wordlist_id"` // 0 表示不限词表
	EntryIDs         []int64 `json:"entry_ids"`   // 选中的条目（规范序，只增不减）
	ListenerIDs      []int64 `json:"listener_ids"`
	Status           string  `json:"status"`
	Seed             int64   `json:"seed"`
	// CurrentPosition 断点续发游标：已编排到第几个试次位置。
	// 中断后再发只从该游标之后继续，已收作答的试次永不重排。
	CurrentPosition int       `json:"current_position"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ListeningTrial 一条试次：某个条目在该调查点下对应的一段实际音频。
// Position 是该实验内全局递增的位置号（1 起）；作废不移位、不回收，补位 trial
// 追加新 Position，这样打乱编排可复现、可审计，旧位置永不复用。
type ListeningTrial struct {
	ID             int64      `json:"id"`
	ExperimentID   int64      `json:"experiment_id"`
	EntryID        int64      `json:"entry_id"`
	SegmentID      int64      `json:"segment_id"`
	ObjectKey      string     `json:"object_key"`
	Position       int        `json:"position"`
	BatchNo        int        `json:"batch_no"`
	Status         string     `json:"status"`
	VoidReason     string     `json:"void_reason,omitempty"`
	VoidedAt       *time.Time `json:"voided_at,omitempty"`
	ReplacementFor int64      `json:"replacement_for,omitempty"` // 非 0：本试次补的是哪条作废 trial
	CreatedAt      time.Time  `json:"created_at"`
}

// ListeningAssignment 把一条试次发给某个听辨人，并记录在该听辨人序列中的次序。
// OrderIndex 每人各自从 1 递增；同一 trial 对不同 listener 的 OrderIndex 因轮换而不同。
type ListeningAssignment struct {
	ID           int64      `json:"id"`
	ExperimentID int64      `json:"experiment_id"`
	ListenerID   int64      `json:"listener_id"`
	TrialID      int64      `json:"trial_id"`
	OrderIndex   int        `json:"order_index"`
	Offset       int        `json:"offset"` // 该听辨人的轮换偏移（Latin-square rank）
	Status       string     `json:"status"`
	IssuedAt     *time.Time `json:"issued_at,omitempty"`
	AnsweredAt   *time.Time `json:"answered_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// ListeningResponse 听辨人对一条试次的作答。
// DB 上 (listener_id, trial_id) 唯一：同一人对同一条只留第一次，重复提交在数据库层并掉。
type ListeningResponse struct {
	ID           int64     `json:"id"`
	ExperimentID int64     `json:"experiment_id"`
	AssignmentID int64     `json:"assignment_id"`
	ListenerID   int64     `json:"listener_id"`
	TrialID      int64     `json:"trial_id"`
	EntryID      int64     `json:"entry_id"`
	Answer       string    `json:"answer"`
	Confidence   int       `json:"confidence,omitempty"`
	Note         string    `json:"note,omitempty"`
	ClientToken  string    `json:"client_token,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// QueueItem 发给听辨人的队列中的一项（不暴露他人作答与正确答案）。
type QueueItem struct {
	AssignmentID int64  `json:"assignment_id"`
	TrialID      int64  `json:"trial_id"`
	EntryID      int64  `json:"entry_id"`
	ObjectKey    string `json:"object_key"`
	OrderIndex   int    `json:"order_index"`
	Status       string `json:"status"`
	Answered     bool   `json:"answered"`
}

// ListenerProgress 单个听辨人的收发情况。
type ListenerProgress struct {
	ListenerID      int64   `json:"listener_id"`
	CodeName        string  `json:"code_name"`
	Assigned        int     `json:"assigned"`
	Answered        int     `json:"answered"`
	Pending         int     `json:"pending"`
	MissingEntryIDs []int64 `json:"missing_entry_ids"` // 已发未答的条目
	MissingTrialIDs []int64 `json:"missing_trial_ids"`
}

// ListeningProgress 整个实验的进度：一眼看出缺谁、缺哪几条。
type ListeningProgress struct {
	ExperimentID     int64              `json:"experiment_id"`
	Status           string             `json:"status"`
	TrialTotal       int                `json:"trial_total"`
	TrialActive      int                `json:"trial_active"`
	TrialVoided      int                `json:"trial_voided"`
	AssignmentsTotal int                `json:"assignments_total"`
	Answered         int                `json:"answered"`
	Pending          int                `json:"pending"`
	Complete         bool               `json:"complete"`
	Listeners        []ListenerProgress `json:"listeners"`
}
