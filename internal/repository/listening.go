package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"

	"cc-053/internal/models"
)

type ListeningRepo struct {
	db *sql.DB
}

func NewListeningRepo(db *sql.DB) *ListeningRepo {
	return &ListeningRepo{db: db}
}

// ---------- experiments ----------

func (r *ListeningRepo) CreateExperiment(e *models.ListeningExperiment) error {
	entryIDs, err := json.Marshal(e.EntryIDs)
	if err != nil {
		return err
	}
	listeners, err := json.Marshal(e.Listeners)
	if err != nil {
		return err
	}
	return r.db.QueryRow(
		`INSERT INTO listening_experiments (name, dialect_point_code, wordlist_id, seed, entry_ids, listeners, status)
		 VALUES ($1, $2, $3, $4, $5, $6, 'draft')
		 RETURNING id, status, created_at, updated_at`,
		e.Name, e.DialectPointCode, e.WordlistID, e.Seed, entryIDs, listeners,
	).Scan(&e.ID, &e.Status, &e.CreatedAt, &e.UpdatedAt)
}

func (r *ListeningRepo) GetExperimentByID(id int64) (*models.ListeningExperiment, error) {
	e := &models.ListeningExperiment{}
	var entryIDs, listeners []byte
	err := r.db.QueryRow(
		`SELECT id, name, dialect_point_code, wordlist_id, seed, entry_ids, listeners, status, created_at, updated_at
		 FROM listening_experiments WHERE id=$1`, id,
	).Scan(&e.ID, &e.Name, &e.DialectPointCode, &e.WordlistID, &e.Seed, &entryIDs, &listeners, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(entryIDs, &e.EntryIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(listeners, &e.Listeners); err != nil {
		return nil, err
	}
	return e, nil
}

func (r *ListeningRepo) ListExperiments(offset, limit int) ([]*models.ListeningExperiment, int, error) {
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM listening_experiments`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(
		`SELECT id, name, dialect_point_code, wordlist_id, seed, entry_ids, listeners, status, created_at, updated_at
		 FROM listening_experiments ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var experiments []*models.ListeningExperiment
	for rows.Next() {
		e := &models.ListeningExperiment{}
		var entryIDs, listeners []byte
		if err := rows.Scan(&e.ID, &e.Name, &e.DialectPointCode, &e.WordlistID, &e.Seed, &entryIDs, &listeners, &e.Status, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(entryIDs, &e.EntryIDs); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(listeners, &e.Listeners); err != nil {
			return nil, 0, err
		}
		experiments = append(experiments, e)
	}
	return experiments, total, nil
}

func (r *ListeningRepo) UpdateExperimentStatus(id int64, status string) error {
	_, err := r.db.Exec(
		`UPDATE listening_experiments SET status=$1, updated_at=NOW() WHERE id=$2`, status, id,
	)
	return err
}

// AddEntryIDs 把补位条目并入实验的条目集（去重），供后续轮次使用。
func (r *ListeningRepo) AddEntryIDs(id int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.db.Exec(
		`UPDATE listening_experiments
		 SET entry_ids = (
			 SELECT COALESCE(jsonb_agg(x), '[]'::jsonb) FROM (
				 SELECT DISTINCT x FROM (
					 SELECT jsonb_array_elements_text(entry_ids)::bigint AS x
					 FROM listening_experiments WHERE id=$1
					 UNION
					 SELECT unnest($2::bigint[])
				 ) u ORDER BY x
			 ) s
		 ),
		 updated_at=NOW()
		 WHERE id=$1`, id, pq.Array(ids),
	)
	return err
}

// ---------- trials ----------

// InsertTrialsBatch 批量下发试次。靠 (experiment_id, round, listener, entry_id)
// 唯一约束幂等：断点续发时已存在的试次（含已作答的）被跳过，不会重来。
// 返回实际新插入的条数。
func (r *ListeningRepo) InsertTrialsBatch(trials []*models.ListeningTrial) (int, error) {
	if len(trials) == 0 {
		return 0, nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO listening_trials (experiment_id, round, listener, entry_id, position, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending')
		 ON CONFLICT (experiment_id, round, listener, entry_id) DO NOTHING`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, t := range trials {
		res, err := stmt.Exec(t.ExperimentID, t.Round, t.Listener, t.EntryID, t.Position)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (r *ListeningRepo) GetTrialByID(id int64) (*models.ListeningTrial, error) {
	t := &models.ListeningTrial{}
	err := r.db.QueryRow(
		`SELECT id, experiment_id, round, listener, entry_id, position, status, created_at, updated_at
		 FROM listening_trials WHERE id=$1`, id,
	).Scan(&t.ID, &t.ExperimentID, &t.Round, &t.Listener, &t.EntryID, &t.Position, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListTrials 按实验列出试次，可按听辨人 / 状态过滤，按 (listener, round, position) 排序。
func (r *ListeningRepo) ListTrials(experimentID int64, listener, status string) ([]*models.ListeningTrial, error) {
	query := `SELECT id, experiment_id, round, listener, entry_id, position, status, created_at, updated_at
		 FROM listening_trials WHERE experiment_id=$1`
	args := []interface{}{experimentID}
	n := 1
	if listener != "" {
		n++
		query += fmt.Sprintf(" AND listener=$%d", n)
		args = append(args, listener)
	}
	if status != "" {
		n++
		query += fmt.Sprintf(" AND status=$%d", n)
		args = append(args, status)
	}
	query += " ORDER BY listener, round, position, id"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trials []*models.ListeningTrial
	for rows.Next() {
		t := &models.ListeningTrial{}
		if err := rows.Scan(&t.ID, &t.ExperimentID, &t.Round, &t.Listener, &t.EntryID, &t.Position, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		trials = append(trials, t)
	}
	return trials, nil
}

// MarkAnsweredByListenerEntry 把某听辨人对某条目的所有待答试次标记为已答。
// 同一 (listener, entry) 只留第一次作答，因此其下所有待答试次一并结清。
func (r *ListeningRepo) MarkAnsweredByListenerEntry(experimentID int64, listener string, entryID int64) error {
	_, err := r.db.Exec(
		`UPDATE listening_trials SET status='answered', updated_at=NOW()
		 WHERE experiment_id=$1 AND listener=$2 AND entry_id=$3 AND status='pending'`,
		experimentID, listener, entryID,
	)
	return err
}

// VoidTrials 作废一批试次；只允许作废 pending 的（已作答的不再变动）。返回作废条数。
func (r *ListeningRepo) VoidTrials(experimentID int64, ids []int64) (int64, error) {
	res, err := r.db.Exec(
		`UPDATE listening_trials SET status='voided', updated_at=NOW()
		 WHERE experiment_id=$1 AND id = ANY($2) AND status='pending'`,
		experimentID, pq.Array(ids),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MaxRound 当前最大轮次；还没有试次时返回 0。
func (r *ListeningRepo) MaxRound(experimentID int64) (int, error) {
	var round int
	err := r.db.QueryRow(
		`SELECT COALESCE(MAX(round), 0) FROM listening_trials WHERE experiment_id=$1`, experimentID,
	).Scan(&round)
	return round, err
}

// OwedEntries 查出"缺位待补"的 (listener, entry_id)：
// 该 (listener, entry) 最新一次试次是 voided，且尚未留下作答。
func (r *ListeningRepo) OwedEntries(experimentID int64, listener string) (map[string][]int64, error) {
	query := `SELECT t.listener, t.entry_id
		 FROM listening_trials t
		 WHERE t.experiment_id=$1 AND t.status='voided'
		   AND NOT EXISTS (
			   SELECT 1 FROM listening_trials t2
			   WHERE t2.experiment_id=t.experiment_id AND t2.listener=t.listener
			     AND t2.entry_id=t.entry_id AND t2.round > t.round
		   )
		   AND NOT EXISTS (
			   SELECT 1 FROM listening_responses resp
			   WHERE resp.experiment_id=t.experiment_id AND resp.listener=t.listener
			     AND resp.entry_id=t.entry_id
		   )`
	args := []interface{}{experimentID}
	if listener != "" {
		query += " AND t.listener=$2"
		args = append(args, listener)
	}
	query += " ORDER BY t.listener, t.entry_id"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	owed := map[string][]int64{}
	for rows.Next() {
		var l string
		var entryID int64
		if err := rows.Scan(&l, &entryID); err != nil {
			return nil, err
		}
		owed[l] = append(owed[l], entryID)
	}
	return owed, nil
}

// PendingEntryIDs 某听辨人当前待答的条目集，用于补位时去重。
func (r *ListeningRepo) PendingEntryIDs(experimentID int64, listener string) (map[int64]bool, error) {
	rows, err := r.db.Query(
		`SELECT entry_id FROM listening_trials
		 WHERE experiment_id=$1 AND listener=$2 AND status='pending'`,
		experimentID, listener,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	set := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, nil
}

// ---------- responses ----------

// InsertResponseFirst 插入作答；命中 (experiment_id, listener, entry_id)
// 唯一约束时 DO NOTHING —— 同一人对同一条只留第一次。
// 返回是否真正插入（false = 重复提交被并掉）。
func (r *ListeningRepo) InsertResponseFirst(resp *models.ListeningResponse) (bool, error) {
	err := r.db.QueryRow(
		`INSERT INTO listening_responses (experiment_id, trial_id, listener, entry_id, choice, note)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (experiment_id, listener, entry_id) DO NOTHING
		 RETURNING id, created_at`,
		resp.ExperimentID, resp.TrialID, resp.Listener, resp.EntryID, resp.Choice, resp.Note,
	).Scan(&resp.ID, &resp.CreatedAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *ListeningRepo) GetResponse(experimentID int64, listener string, entryID int64) (*models.ListeningResponse, error) {
	resp := &models.ListeningResponse{}
	err := r.db.QueryRow(
		`SELECT id, experiment_id, trial_id, listener, entry_id, choice, note, created_at
		 FROM listening_responses
		 WHERE experiment_id=$1 AND listener=$2 AND entry_id=$3`,
		experimentID, listener, entryID,
	).Scan(&resp.ID, &resp.ExperimentID, &resp.TrialID, &resp.Listener, &resp.EntryID, &resp.Choice, &resp.Note, &resp.CreatedAt)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (r *ListeningRepo) ListResponses(experimentID int64, listener string) ([]*models.ListeningResponse, error) {
	query := `SELECT id, experiment_id, trial_id, listener, entry_id, choice, note, created_at
		 FROM listening_responses WHERE experiment_id=$1`
	args := []interface{}{experimentID}
	if listener != "" {
		query += " AND listener=$2"
		args = append(args, listener)
	}
	query += " ORDER BY listener, entry_id"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []*models.ListeningResponse
	for rows.Next() {
		resp := &models.ListeningResponse{}
		if err := rows.Scan(&resp.ID, &resp.ExperimentID, &resp.TrialID, &resp.Listener, &resp.EntryID, &resp.Choice, &resp.Note, &resp.CreatedAt); err != nil {
			return nil, err
		}
		responses = append(responses, resp)
	}
	return responses, nil
}
