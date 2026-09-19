package models

// CreateListeningExperimentRequest 创建听辨实验。
//
// 从指定调查点挑条目：EntryIDs 为空时，取该调查点（可限词表）已切分完成
// （segments.status='completed'）的全部分布条目；显式给 EntryIDs 则只挑这些条目。
type CreateListeningExperimentRequest struct {
	Name             string  `json:"name" binding:"required"`
	DialectPointCode string  `json:"dialect_point_code" binding:"required"`
	WordlistID       int64   `json:"wordlist_id"`
	EntryIDs         []int64 `json:"entry_ids"`
	ListenerIDs      []int64 `json:"listener_ids" binding:"required,min=1"`
	Seed             int64   `json:"seed"` // 0 则由服务端生成
	// BatchSize 首批编排多少个试次位置；0 表示全部一次排完。
	// 支持"先发一批、断过之后再接着发"。
	BatchSize int `json:"batch_size"`
}

// IssueListeningBatchRequest 断点续发：从当前位置之后接着编排下一批。
// 已发出/已作答的试次不动，绝不重排重来。
type IssueListeningBatchRequest struct {
	BatchSize int `json:"batch_size"` // 0 表示把剩余条目全部发完
}

// ListeningResponseRequest 听辨人提交作答。
// TrialID 必填；Answer 为听辨内容（本字/释义/调类等由实验约定）。
// ClientToken 用于客户端重试幂等（可空，服务端另有唯一约束兜底）。
type ListeningResponseRequest struct {
	TrialID     int64  `json:"trial_id" binding:"required"`
	Answer      string `json:"answer" binding:"required"`
	Confidence  int    `json:"confidence"`
	Note        string `json:"note"`
	ClientToken string `json:"client_token"`
}

// VoidTrialRequest 中途作废一条试次（音频损坏、读错等）。
type VoidTrialRequest struct {
	Reason string `json:"reason" binding:"required"`
	// Replace: 是否立即重排补位。true 时为每个已收到该 trial 的听辨人
	// 生成补位试次（原条目在该调查点的另一段音频，或仍缺音频时返回未覆盖名单）。
	Replace bool `json:"replace"`
}
