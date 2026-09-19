package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"cc-053/internal/listenplan"
	"cc-053/internal/models"
)

var (
	ErrListeningNotFound       = errors.New("listening experiment not found")
	ErrListeningNotActive      = errors.New("listening experiment is not active")
	ErrTrialNotFound           = errors.New("listening trial not found")
	ErrTrialAlreadyVoided      = errors.New("listening trial already voided")
	ErrTrialNotVoided          = errors.New("listening trial is not voided")
	ErrTrialAlreadyReplaced    = errors.New("voided trial already has an active replacement")
	ErrAssignmentNotFound      = errors.New("assignment not found: trial was not issued to this listener")
	ErrTrialVoided             = errors.New("trial has been voided")
	ErrListeningHasPending     = errors.New("experiment still has pending assignments")
	ErrListeningUncovered      = errors.New("voided trials still lack replacements for some listeners")
	ErrListeningNothingToIssue = errors.New("no remaining entries to issue")
)

// TrialBrief 编排结果中的试次摘要。
type TrialBrief struct {
	TrialID   int64  `json:"trial_id"`
	EntryID   int64  `json:"entry_id"`
	Position  int    `json:"position"`
	ObjectKey string `json:"object_key"`
}

// BatchIssueResult 一次发批的结果。
type BatchIssueResult struct {
	BatchNo       int          `json:"batch_no"`
	StartPosition int          `json:"start_position"`
	EndPosition   int          `json:"end_position"`
	Trials        []TrialBrief `json:"trials"`
}

// ReplacementResult 作废（并可补位）的结果。
type ReplacementResult struct {
	VoidedTrialID        int64   `json:"voided_trial_id"`
	ReplacementTrialID   int64   `json:"replacement_trial_id,omitempty"`
	EntryID              int64   `json:"entry_id"`
	Position             int     `json:"position,omitempty"`
	AssignedListenerIDs  []int64 `json:"assigned_listener_ids,omitempty"`
	UncoveredListenerIDs []int64 `json:"uncovered_listener_ids,omitempty"` // 无替代音频、仍缺补位的听辨人
}

type ListeningRepo struct {
	db *sql.DB
}

func NewListeningRepo(db *sql.DB) *ListeningRepo {
	return &ListeningRepo{db: db}
}

// ---------------- 实验 ----------------

func (r *ListeningRepo) CreateExperiment(e *models.ListeningExperiment) error {
	return r.db.QueryRow(
		`INSERT INTO listening_experiments (name, dialect_point_code, wordlist_id, entry_ids, listener_ids, status, seed, current_position)
		 VALUES ($1, $2, $3, $4, $5, 'active', $6, 0)
		 RETURNING id, status, current_position, created_at, updated_at`,
		e.Name, e.DialectPointCode, e.WordlistID, pq.Array(e.EntryIDs), pq.Array(e.ListenerIDs), e.Seed,
	).Scan(&e.ID, &e.Status, &e.CurrentPosition, &e.CreatedAt, &e.UpdatedAt)
}

const experimentColumns = `id, name, dialect_point_code, wordlist_id, entry_ids, listener_ids, status, seed, current_position, created_at, updated_at`

func (r *ListeningRepo) scanExperiment(row interface{ Scan(...interface{}) error }) (*models.ListeningExperiment, error) {
	e := &models.ListeningExperiment{}
	err := row.Scan(
		&e.ID, &e.Name, &e.DialectPointCode, &e.WordlistID,
		pq.Array(&e.EntryIDs), pq.Array(&e.ListenerIDs),
		&e.Status, &e.Seed, &e.CurrentPosition, &e.CreatedAt, &e.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrListeningNotFound
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (r *ListeningRepo) GetExperiment(id int64) (*models.ListeningExperiment, error) {
	return r.scanExperiment(r.db.QueryRow(
		fmt.Sprintf(`SELECT %s FROM listening_experiments WHERE id=$1`, experimentColumns), id,
	))
}

func (r *ListeningRepo) ListExperiments(offset, limit int) ([]*models.ListeningExperiment, int, error) {
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM listening_experiments`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(
		fmt.Sprintf(`SELECT %s FROM listening_experiments ORDER BY id DESC LIMIT $1 OFFSET $2`, experimentColumns),
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*models.ListeningExperiment
	for rows.Next() {
		e, err := r.scanExperiment(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, nil
}

// ---------------- 条目 / 听辨人校验 ----------------

// DiscoverAvailableEntries 返回某调查点（可限词表）下已有"切分完成"音频的条目，
// 按 entry_id 升序。创建实验时用它挑条目，保证被选中的条目都能真正发出去。
func (r *ListeningRepo) DiscoverAvailableEntries(dialectPoint string, wordlistID int64) ([]int64, error) {
	rows, err := r.db.Query(
		`SELECT DISTINCT s.entry_id
		 FROM segments s
		 JOIN recordings rec ON rec.id = s.recording_id
		 JOIN tasks t ON t.id = rec.task_id
		 JOIN speakers sp ON sp.id = t.speaker_id
		 WHERE sp.dialect_point_code = $1
		   AND s.status = 'completed'
		   AND ($2 = 0 OR t.wordlist_id = $2)
		 ORDER BY s.entry_id`,
		dialectPoint, wordlistID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// EntriesMissingAudio 给出给定条目中在该调查点下没有"切分完成"音频的部分。
func (r *ListeningRepo) EntriesMissingAudio(dialectPoint string, wordlistID int64, entryIDs []int64) ([]int64, error) {
	available, err := r.DiscoverAvailableEntries(dialectPoint, wordlistID)
	if err != nil {
		return nil, err
	}
	have := map[int64]bool{}
	for _, id := range available {
		have[id] = true
	}
	var missing []int64
	seen := map[int64]bool{}
	for _, id := range entryIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if !have[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// CheckListenersExist 校验听辨人都存在（speakers 表），返回不存在的 ID。
func (r *ListeningRepo) CheckListenersExist(ids []int64) ([]int64, error) {
	rows, err := r.db.Query(`SELECT id FROM speakers WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = true
	}
	var missing []int64
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if !found[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// ---------------- 音频解析 ----------------

// queryer 让同一套查询既能跑在 *sql.DB 上也能跑在事务里。
type queryer interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// segmentAudio 调查点下某条目的一段可用音频。
type segmentAudio struct {
	EntryID   int64
	SegmentID int64
	ObjectKey string
}

// resolveAudio 按调查点（可限词表）为给定条目各取一段已完成切分的音频。
// 同一调查点同一条目可能有多位发音人/多段录音，取 segment id 最小者，保证可复现。
func resolveAudio(q queryer, dialectPoint string, wordlistID int64, entryIDs []int64) (map[int64]segmentAudio, error) {
	out := map[int64]segmentAudio{}
	if len(entryIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(
		`SELECT DISTINCT ON (s.entry_id) s.entry_id, s.id, s.object_key
		 FROM segments s
		 JOIN recordings rec ON rec.id = s.recording_id
		 JOIN tasks t ON t.id = rec.task_id
		 JOIN speakers sp ON sp.id = t.speaker_id
		 WHERE sp.dialect_point_code = $1
		   AND s.status = 'completed'
		   AND s.entry_id = ANY($2)
		   AND ($3 = 0 OR t.wordlist_id = $3)
		 ORDER BY s.entry_id, s.id`,
		dialectPoint, pq.Array(entryIDs), wordlistID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a segmentAudio
		if err := rows.Scan(&a.EntryID, &a.SegmentID, &a.ObjectKey); err != nil {
			return nil, err
		}
		out[a.EntryID] = a
	}
	return out, nil
}

// resolveAlternativeAudio 为某条目找一段该实验从未用过的替代音频（补位用）。
// 已被任何试次（含已作废）用过的段不再选用——已知损坏的音频绝不重发。
func resolveAlternativeAudio(q queryer, dialectPoint string, experimentID, entryID, excludeSegmentID int64) (segmentAudio, bool, error) {
	var a segmentAudio
	err := q.QueryRow(
		`SELECT s.entry_id, s.id, s.object_key
		 FROM segments s
		 JOIN recordings rec ON rec.id = s.recording_id
		 JOIN tasks t ON t.id = rec.task_id
		 JOIN speakers sp ON sp.id = t.speaker_id
		 WHERE sp.dialect_point_code = $1
		   AND s.entry_id = $2
		   AND s.status = 'completed'
		   AND s.id <> $3
		   AND NOT EXISTS (
		       SELECT 1 FROM listening_trials lt
		       WHERE lt.experiment_id = $4 AND lt.segment_id = s.id
		   )
		 ORDER BY s.id LIMIT 1`,
		dialectPoint, entryID, excludeSegmentID, experimentID,
	).Scan(&a.EntryID, &a.SegmentID, &a.ObjectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return segmentAudio{}, false, nil
	}
	if err != nil {
		return segmentAudio{}, false, err
	}
	return a, true, nil
}

// ---------------- 发批（断点续发） ----------------

// IssueBatch 从 current_position 之后接着编排下一批。
// 已发出/已作答的试次一律不动；只追加新试次与新分发。
// 全局位置号取 MAX(position)+1，与消费游标解耦：作废补位追加的位置不会与后续批次相撞。
func (r *ListeningRepo) IssueBatch(experimentID int64, batchSize int) (*BatchIssueResult, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	e, err := r.scanExperiment(tx.QueryRow(
		fmt.Sprintf(`SELECT %s FROM listening_experiments WHERE id=$1 FOR UPDATE`, experimentColumns), experimentID,
	))
	if err != nil {
		return nil, err
	}
	if e.Status != models.ListeningStatusActive {
		return nil, ErrListeningNotActive
	}

	// 规范序 → 种子打乱；只取游标之后的条目，已发的永不重排。
	shuffled := listenplan.ShuffleEntries(e.EntryIDs, e.Seed)
	if e.CurrentPosition >= len(shuffled) {
		return nil, ErrListeningNothingToIssue
	}
	remaining := shuffled[e.CurrentPosition:]
	if batchSize > 0 && batchSize < len(remaining) {
		remaining = remaining[:batchSize]
	}

	audio, err := resolveAudio(tx, e.DialectPointCode, e.WordlistID, remaining)
	if err != nil {
		return nil, err
	}
	// 选条目时已校验音频存在；此处做防御性检查，缺音频说明数据被后台改动。
	picked := make([]segmentAudio, len(remaining))
	for i, entryID := range remaining {
		a, ok := audio[entryID]
		if !ok {
			return nil, fmt.Errorf("entry %d 在调查点 %s 已无可用完成音频（可能被清理），请先补录再发批", entryID, e.DialectPointCode)
		}
		picked[i] = a
	}

	// 听辨人 rank 按 ID 升序固定，保证各批轮换偏移一致。
	sortedListeners := append([]int64(nil), e.ListenerIDs...)
	sortInt64(sortedListeners)
	ranks := make([]listenplan.ListenerRank, len(sortedListeners))
	for i, id := range sortedListeners {
		ranks[i] = listenplan.ListenerRank{ID: id, Rank: i}
	}

	// 每位听辨人此前已拿到的最大个人号位（含作废，旧号位不复用）→ 新批次从 max+1 接着编号。
	baseOrder := map[int64]int{}
	rows, err := tx.Query(
		`SELECT listener_id, COALESCE(MAX(order_index),0) FROM listening_assignments
		 WHERE experiment_id=$1 GROUP BY listener_id`,
		experimentID,
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var lid int64
		var n int
		if err := rows.Scan(&lid, &n); err != nil {
			rows.Close()
			return nil, err
		}
		baseOrder[lid] = n
	}
	rows.Close()

	var batchNo int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(batch_no),0)+1 FROM listening_trials WHERE experiment_id=$1`, experimentID,
	).Scan(&batchNo); err != nil {
		return nil, err
	}
	var startPosition int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position),0)+1 FROM listening_trials WHERE experiment_id=$1`, experimentID,
	).Scan(&startPosition); err != nil {
		return nil, err
	}

	plan := listenplan.PlanBatch(remaining, startPosition, ranks, baseOrder)
	res := &BatchIssueResult{BatchNo: batchNo, StartPosition: startPosition}

	for slot, pt := range plan.Trials {
		au := picked[slot]
		var trialID int64
		if err := tx.QueryRow(
			`INSERT INTO listening_trials (experiment_id, entry_id, segment_id, object_key, position, batch_no, status)
			 VALUES ($1, $2, $3, $4, $5, $6, 'active') RETURNING id`,
			experimentID, pt.EntryID, au.SegmentID, au.ObjectKey, pt.Position, batchNo,
		).Scan(&trialID); err != nil {
			return nil, err
		}
		res.Trials = append(res.Trials, TrialBrief{
			TrialID: trialID, EntryID: pt.EntryID, Position: pt.Position, ObjectKey: au.ObjectKey,
		})
		res.EndPosition = pt.Position

		for _, as := range plan.Assignments[slot] {
			if _, err := tx.Exec(
				`INSERT INTO listening_assignments (experiment_id, listener_id, trial_id, order_index, "offset", status, issued_at)
				 VALUES ($1, $2, $3, $4, $5, 'pending', NOW())`,
				experimentID, as.ListenerID, trialID, as.OrderIndex, as.Offset,
			); err != nil {
				return nil, err
			}
		}
	}

	// 游标只按已消费的条目数推进；位置号另由 MAX(position) 分配。
	if _, err := tx.Exec(
		`UPDATE listening_experiments SET current_position = current_position + $1, updated_at=NOW() WHERE id=$2`,
		len(remaining), experimentID,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func sortInt64(s []int64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// ---------------- 队列与作答 ----------------

func (r *ListeningRepo) GetQueue(experimentID, listenerID int64, status string) ([]*models.QueueItem, error) {
	q := `SELECT a.id, a.trial_id, t.entry_id, t.object_key, a.order_index, a.status
	      FROM listening_assignments a
	      JOIN listening_trials t ON t.id = a.trial_id
	      WHERE a.experiment_id=$1 AND a.listener_id=$2`
	switch status {
	case "pending":
		q += ` AND a.status='pending' AND t.status='active'`
	case "answered":
		q += ` AND a.status='answered'`
	case "voided":
		q += ` AND a.status='voided'`
	}
	// 补位复用原号位时，该号位会并存"旧作废行 + 新待答行"；同号位把作废行排后。
	q += ` ORDER BY a.order_index, CASE a.status WHEN 'voided' THEN 1 ELSE 0 END, a.id`

	rows, err := r.db.Query(q, experimentID, listenerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.QueueItem
	for rows.Next() {
		item := &models.QueueItem{}
		if err := rows.Scan(&item.AssignmentID, &item.TrialID, &item.EntryID, &item.ObjectKey, &item.OrderIndex, &item.Status); err != nil {
			return nil, err
		}
		item.Answered = item.Status == "answered"
		out = append(out, item)
	}
	return out, nil
}

const responseColumns = `id, experiment_id, assignment_id, listener_id, trial_id, entry_id, answer, confidence, note, client_token, created_at`

func scanResponse(row interface{ Scan(...interface{}) error }, resp *models.ListeningResponse) error {
	return row.Scan(&resp.ID, &resp.ExperimentID, &resp.AssignmentID, &resp.ListenerID,
		&resp.TrialID, &resp.EntryID, &resp.Answer, &resp.Confidence, &resp.Note,
		&resp.ClientToken, &resp.CreatedAt)
}

// isUniqueViolation 判断 Postgres 23505 唯一约束冲突。
func isUniqueViolation(err error) bool {
	var pgErr *pq.Error
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// SubmitResponse 提交作答。
// 返回 (response, duplicated)：同一人对同一条重复提交时不新增行，duplicated=true，
// 返回第一次的作答，由调用方提示"已并掉重复提交"。
func (r *ListeningRepo) SubmitResponse(experimentID, listenerID int64, req models.ListeningResponseRequest) (*models.ListeningResponse, bool, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var expStatus string
	if err := tx.QueryRow(`SELECT status FROM listening_experiments WHERE id=$1`, experimentID).Scan(&expStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, ErrListeningNotFound
		}
		return nil, false, err
	}
	if expStatus != models.ListeningStatusActive {
		return nil, false, ErrListeningNotActive
	}

	// client_token 重试：同一令牌直接返回首次结果（覆盖并发竞争）。
	if req.ClientToken != "" {
		existing := &models.ListeningResponse{}
		err := scanResponse(tx.QueryRow(
			fmt.Sprintf(`SELECT %s FROM listening_responses WHERE experiment_id=$1 AND client_token=$2`, responseColumns),
			experimentID, req.ClientToken,
		), existing)
		if err == nil {
			return existing, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}

	var assignmentID, entryID int64
	var assignStatus, trialStatus string
	err = tx.QueryRow(
		`SELECT a.id, t.entry_id, a.status, t.status
		 FROM listening_assignments a
		 JOIN listening_trials t ON t.id = a.trial_id
		 WHERE a.experiment_id=$1 AND a.listener_id=$2 AND a.trial_id=$3
		 FOR UPDATE OF a`,
		experimentID, listenerID, req.TrialID,
	).Scan(&assignmentID, &entryID, &assignStatus, &trialStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrAssignmentNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if trialStatus == models.TrialStatusVoided || assignStatus == models.AssignStatusVoided {
		return nil, false, ErrTrialVoided
	}

	resp := &models.ListeningResponse{}
	// 唯一约束 (listener_id, trial_id) 兜底并发：DO NOTHING，重复时回读第一次。
	insertErr := tx.QueryRow(
		`INSERT INTO listening_responses
		   (experiment_id, assignment_id, listener_id, trial_id, entry_id, answer, confidence, note, client_token)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (listener_id, trial_id) DO NOTHING
		 RETURNING id, created_at`,
		experimentID, assignmentID, listenerID, req.TrialID, entryID,
		req.Answer, req.Confidence, req.Note, req.ClientToken,
	).Scan(&resp.ID, &resp.CreatedAt)

	duplicated := false
	switch {
	case insertErr == nil:
		// 首次作答落库成功
	case errors.Is(insertErr, sql.ErrNoRows):
		// (listener_id, trial_id) 已存在（含并发抢先）→ 并掉
		duplicated = true
	case isUniqueViolation(insertErr):
		// client_token 在并发下与另一试次的提交相撞 → 按令牌回读首次结果
		if req.ClientToken == "" {
			return nil, false, insertErr
		}
		duplicated = true
	default:
		return nil, false, insertErr
	}

	if duplicated {
		lookup := func(q string, args ...interface{}) (*models.ListeningResponse, error) {
			existing := &models.ListeningResponse{}
			if err := scanResponse(tx.QueryRow(q, args...), existing); err != nil {
				return nil, err
			}
			return existing, nil
		}
		if req.ClientToken != "" {
			resp, err = lookup(
				fmt.Sprintf(`SELECT %s FROM listening_responses WHERE experiment_id=$1 AND client_token=$2`, responseColumns),
				experimentID, req.ClientToken,
			)
		} else {
			resp, err = lookup(
				fmt.Sprintf(`SELECT %s FROM listening_responses WHERE listener_id=$1 AND trial_id=$2`, responseColumns),
				listenerID, req.TrialID,
			)
		}
		if err != nil {
			return nil, false, err
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE listening_assignments SET status='answered', answered_at=NOW()
			 WHERE id=$1 AND status='pending'`, assignmentID,
		); err != nil {
			return nil, false, err
		}
		resp.ExperimentID = experimentID
		resp.AssignmentID = assignmentID
		resp.ListenerID = listenerID
		resp.TrialID = req.TrialID
		resp.EntryID = entryID
		resp.Answer = req.Answer
		resp.Confidence = req.Confidence
		resp.Note = req.Note
		resp.ClientToken = req.ClientToken
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return resp, duplicated, nil
}

// ---------------- 作废与补位 ----------------

// listenerRank 返回听辨人按 ID 升序后的固定 rank（与发批时一致）。
func listenerRank(q queryer, experimentID, listenerID int64) (int, error) {
	var rank int
	err := q.QueryRow(
		`SELECT array_position(ARRAY(SELECT unnest(listener_ids) ORDER BY 1), $1) - 1
		 FROM listening_experiments WHERE id=$2`,
		listenerID, experimentID,
	).Scan(&rank)
	return rank, err
}

func (r *ListeningRepo) VoidTrial(experimentID, trialID int64, reason string, replace bool) (*ReplacementResult, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var expStatus, dialectPoint, trialStatus string
	var entryID, segmentID int64
	err = tx.QueryRow(
		`SELECT e.status, e.dialect_point_code, t.entry_id, t.segment_id, t.status
		 FROM listening_trials t JOIN listening_experiments e ON e.id = t.experiment_id
		 WHERE t.experiment_id=$1 AND t.id=$2 FOR UPDATE OF t`,
		experimentID, trialID,
	).Scan(&expStatus, &dialectPoint, &entryID, &segmentID, &trialStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrialNotFound
	}
	if err != nil {
		return nil, err
	}
	if expStatus != models.ListeningStatusActive {
		return nil, ErrListeningNotActive
	}
	if trialStatus != models.TrialStatusActive {
		return nil, ErrTrialAlreadyVoided
	}

	if _, err := tx.Exec(
		`UPDATE listening_trials SET status='voided', void_reason=$1, voided_at=NOW() WHERE id=$2`,
		reason, trialID,
	); err != nil {
		return nil, err
	}
	// 所有旧分发都随试次作废（无论是否已答；作答记录保留在 listening_responses 可审计）。
	// 同时记下每位听辨人原来听到这条时的个人号位——补位将精确填回同一号位。
	if _, err := tx.Exec(
		`UPDATE listening_assignments SET status='voided' WHERE trial_id=$1`, trialID,
	); err != nil {
		return nil, err
	}

	var oldAssigns []oldAssignment
	rows, err := tx.Query(`SELECT listener_id, order_index FROM listening_assignments WHERE trial_id=$1 ORDER BY listener_id`, trialID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a oldAssignment
		if err := rows.Scan(&a.listenerID, &a.orderIndex); err != nil {
			rows.Close()
			return nil, err
		}
		oldAssigns = append(oldAssigns, a)
	}
	rows.Close()
	listeners := make([]int64, len(oldAssigns))
	for i, a := range oldAssigns {
		listeners[i] = a.listenerID
	}

	res := &ReplacementResult{VoidedTrialID: trialID, EntryID: entryID, AssignedListenerIDs: listeners}

	if replace && len(oldAssigns) > 0 {
		rep, err := createReplacement(tx, experimentID, trialID, dialectPoint, entryID, segmentID, oldAssigns)
		if err != nil {
			return nil, err
		}
		res.ReplacementTrialID = rep.ReplacementTrialID
		res.Position = rep.Position
		if rep.ReplacementTrialID == 0 {
			res.UncoveredListenerIDs = listeners
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// oldAssignment 记录某作废试次在一位听辨人处原来的号位。
type oldAssignment struct {
	listenerID int64
	orderIndex int
}

// listenersNeedingCoverage 返回 oldAssigns 中当前对该条目已没有任何有效试次的听辨人；
// 已被链条上的后续补位覆盖的听辨人不再重复发放，避免同一人对同一条拿到两个有效试次。
func filterNeedingCoverage(tx *sql.Tx, experimentID, entryID int64, old []oldAssignment) ([]oldAssignment, error) {
	var need []oldAssignment
	for _, oa := range old {
		var covered int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM listening_assignments a
			 JOIN listening_trials t ON t.id = a.trial_id
			 WHERE a.experiment_id=$1 AND a.listener_id=$2 AND t.entry_id=$3
			   AND t.status='active' AND a.status <> 'voided'`,
			experimentID, oa.listenerID, entryID,
		).Scan(&covered); err != nil {
			return nil, err
		}
		if covered == 0 {
			need = append(need, oa)
		}
	}
	return need, nil
}

// createReplacement 在事务内为一条作废试次补位：换一段同条目音频生成新 trial，
// 全局位置追加到尾部，个人号位精确填回各人原号位。无替代音频时返回零值结果。
func createReplacement(tx *sql.Tx, experimentID, voidedTrialID int64, dialectPoint string, entryID, excludeSegmentID int64, oldAssigns []oldAssignment) (*ReplacementResult, error) {
	need, err := filterNeedingCoverage(tx, experimentID, entryID, oldAssigns)
	if err != nil {
		return nil, err
	}
	listeners := make([]int64, len(need))
	for i, a := range need {
		listeners[i] = a.listenerID
	}
	res := &ReplacementResult{VoidedTrialID: voidedTrialID, EntryID: entryID, AssignedListenerIDs: listeners}

	// 所有相关听辨人都已被后续补位覆盖 → 无需再补。
	if len(need) == 0 {
		return res, nil
	}

	alt, ok, err := resolveAlternativeAudio(tx, dialectPoint, experimentID, entryID, excludeSegmentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		res.UncoveredListenerIDs = listeners
		return res, nil
	}

	var position, newBatchNo int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position),0)+1 FROM listening_trials WHERE experiment_id=$1`,
		experimentID,
	).Scan(&position); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(batch_no),0)+1 FROM listening_trials WHERE experiment_id=$1`,
		experimentID,
	).Scan(&newBatchNo); err != nil {
		return nil, err
	}
	var newTrialID int64
	if err := tx.QueryRow(
		`INSERT INTO listening_trials
		   (experiment_id, entry_id, segment_id, object_key, position, batch_no, status, replacement_for)
		 VALUES ($1, $2, $3, $4, $5, $6, 'active', $7) RETURNING id`,
		experimentID, entryID, alt.SegmentID, alt.ObjectKey, position, newBatchNo, voidedTrialID,
	).Scan(&newTrialID); err != nil {
		return nil, err
	}
	for _, oa := range need {
		rank, err := listenerRank(tx, experimentID, oa.listenerID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`INSERT INTO listening_assignments
			   (experiment_id, listener_id, trial_id, order_index, "offset", status, issued_at)
			 VALUES ($1, $2, $3, $4, $5, 'pending', NOW())`,
			experimentID, oa.listenerID, newTrialID, oa.orderIndex, rank,
		); err != nil {
			return nil, err
		}
	}
	res.ReplacementTrialID = newTrialID
	res.Position = position
	return res, nil
}

// ReplaceTrial 为一条已作废、但当时未补位（或补位失败）的试次单独重排补位。
func (r *ListeningRepo) ReplaceTrial(experimentID, trialID int64) (*ReplacementResult, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var expStatus, dialectPoint, trialStatus string
	var entryID, segmentID int64
	err = tx.QueryRow(
		`SELECT e.status, e.dialect_point_code, t.entry_id, t.segment_id, t.status
		 FROM listening_trials t JOIN listening_experiments e ON e.id = t.experiment_id
		 WHERE t.experiment_id=$1 AND t.id=$2 FOR UPDATE OF t`,
		experimentID, trialID,
	).Scan(&expStatus, &dialectPoint, &entryID, &segmentID, &trialStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrialNotFound
	}
	if err != nil {
		return nil, err
	}
	if expStatus != models.ListeningStatusActive {
		return nil, ErrListeningNotActive
	}
	if trialStatus != models.TrialStatusVoided {
		return nil, fmt.Errorf("%w: trial %d is still active; void it before replacing", ErrTrialNotVoided, trialID)
	}

	// 已有有效补位则不重复补。
	var already int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM listening_trials
		 WHERE experiment_id=$1 AND replacement_for=$2 AND status='active'`,
		experimentID, trialID,
	).Scan(&already); err != nil {
		return nil, err
	}
	if already > 0 {
		return nil, ErrTrialAlreadyReplaced
	}

	rows, err := tx.Query(
		`SELECT listener_id, order_index FROM listening_assignments
		 WHERE trial_id=$1 AND status='voided' ORDER BY listener_id`, trialID,
	)
	if err != nil {
		return nil, err
	}
	var oldAssigns []oldAssignment
	for rows.Next() {
		var a oldAssignment
		if err := rows.Scan(&a.listenerID, &a.orderIndex); err != nil {
			rows.Close()
			return nil, err
		}
		oldAssigns = append(oldAssigns, a)
	}
	rows.Close()
	if len(oldAssigns) == 0 {
		return nil, ErrTrialNotFound
	}

	res, err := createReplacement(tx, experimentID, trialID, dialectPoint, entryID, segmentID, oldAssigns)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// ---------------- 试次明细 / 进度 / 收尾 ----------------

func (r *ListeningRepo) ListTrials(experimentID int64) ([]*models.ListeningTrial, error) {
	rows, err := r.db.Query(
		`SELECT id, experiment_id, entry_id, segment_id, object_key, position, batch_no,
		        status, COALESCE(void_reason,''), voided_at, replacement_for, created_at
		 FROM listening_trials WHERE experiment_id=$1 ORDER BY position`, experimentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.ListeningTrial
	for rows.Next() {
		t := &models.ListeningTrial{}
		if err := rows.Scan(&t.ID, &t.ExperimentID, &t.EntryID, &t.SegmentID, &t.ObjectKey,
			&t.Position, &t.BatchNo, &t.Status, &t.VoidReason, &t.VoidedAt, &t.ReplacementFor, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *ListeningRepo) GetProgress(experimentID int64) (*models.ListeningProgress, error) {
	e, err := r.GetExperiment(experimentID)
	if err != nil {
		return nil, err
	}
	p := &models.ListeningProgress{ExperimentID: experimentID, Status: e.Status}

	if err := r.db.QueryRow(
		`SELECT COUNT(*),
		        COUNT(*) FILTER (WHERE status='active'),
		        COUNT(*) FILTER (WHERE status='voided')
		 FROM listening_trials WHERE experiment_id=$1`, experimentID,
	).Scan(&p.TrialTotal, &p.TrialActive, &p.TrialVoided); err != nil {
		return nil, err
	}
	if err := r.db.QueryRow(
		`SELECT COUNT(*),
		        COUNT(*) FILTER (WHERE a.status='answered'),
		        COUNT(*) FILTER (WHERE a.status='pending')
		 FROM listening_assignments a
		 JOIN listening_trials t ON t.id = a.trial_id
		 WHERE a.experiment_id=$1 AND t.status='active'`, experimentID,
	).Scan(&p.AssignmentsTotal, &p.Answered, &p.Pending); err != nil {
		return nil, err
	}
	p.Complete = p.TrialActive > 0 && p.Pending == 0

	// 每位听辨人：已发/已答/待答，以及待答的是哪几条。
	rows, err := r.db.Query(
		`SELECT a.listener_id, sp.code_name,
		        COUNT(*) FILTER (WHERE a.status <> 'voided') AS assigned,
		        COUNT(*) FILTER (WHERE a.status='answered') AS answered,
		        COUNT(*) FILTER (WHERE a.status='pending') AS pending,
		        COALESCE(array_agg(t.entry_id) FILTER (WHERE a.status='pending'), '{}'),
		        COALESCE(array_agg(t.id) FILTER (WHERE a.status='pending'), '{}')
		 FROM listening_assignments a
		 JOIN speakers sp ON sp.id = a.listener_id
		 JOIN listening_trials t ON t.id = a.trial_id
		 WHERE a.experiment_id=$1 AND t.status='active'
		 GROUP BY a.listener_id, sp.code_name
		 ORDER BY a.listener_id`, experimentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		lp := models.ListenerProgress{}
		if err := rows.Scan(&lp.ListenerID, &lp.CodeName, &lp.Assigned, &lp.Answered, &lp.Pending,
			pq.Array(&lp.MissingEntryIDs), pq.Array(&lp.MissingTrialIDs)); err != nil {
			return nil, err
		}
		p.Listeners = append(p.Listeners, lp)
	}
	return p, nil
}

// hasUncoveredVoid 检查是否存在"作废后尚无有效补位"的分发：
// 某听辨人拿到过的作废 trial，在同一条目上还没有对应的 active trial。
func hasUncoveredVoid(q queryer, experimentID int64) (bool, error) {
	var exists bool
	err := q.QueryRow(
		`SELECT EXISTS (
		   SELECT 1
		   FROM listening_assignments av
		   JOIN listening_trials tv ON tv.id = av.trial_id AND tv.status='voided'
		   WHERE av.experiment_id=$1
		     AND NOT EXISTS (
		       SELECT 1 FROM listening_assignments a2
		       JOIN listening_trials t2 ON t2.id = a2.trial_id AND t2.status='active'
		       WHERE a2.listener_id = av.listener_id AND t2.entry_id = tv.entry_id
		     )
		 )`, experimentID,
	).Scan(&exists)
	return exists, err
}

// CompleteExperiment 收尾冻结：尚有未答分发、或作废未补位时拒绝。
func (r *ListeningRepo) CompleteExperiment(experimentID int64) (*models.ListeningProgress, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRow(`SELECT status FROM listening_experiments WHERE id=$1 FOR UPDATE`, experimentID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrListeningNotFound
	}
	if err != nil {
		return nil, err
	}

	var pending int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM listening_assignments a
		 JOIN listening_trials t ON t.id=a.trial_id
		 WHERE a.experiment_id=$1 AND t.status='active' AND a.status='pending'`,
		experimentID,
	).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, fmt.Errorf("%w: 仍有 %d 份未作答", ErrListeningHasPending, pending)
	}
	uncovered, err := hasUncoveredVoid(tx, experimentID)
	if err != nil {
		return nil, err
	}
	if uncovered {
		return nil, ErrListeningUncovered
	}

	if _, err := tx.Exec(
		`UPDATE listening_experiments SET status='completed', updated_at=NOW() WHERE id=$1`,
		experimentID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetProgress(experimentID)
}
