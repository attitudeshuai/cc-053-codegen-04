package handlers

import (
	"crypto/rand"
	"encoding/binary"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"cc-053/internal/models"
	"cc-053/internal/repository"
	"cc-053/internal/services"
)

type ListeningHandler struct {
	repo         *repository.ListeningRepo
	wordlistRepo *repository.WordlistRepo
}

func NewListeningHandler(repo *repository.ListeningRepo, wordlistRepo *repository.WordlistRepo) *ListeningHandler {
	return &ListeningHandler{repo: repo, wordlistRepo: wordlistRepo}
}

// Create 建一批听辨实验：按调查点挑条目、定听辨人、生成种子。此时只编排，不下发。
func (h *ListeningHandler) Create(c *gin.Context) {
	var req models.CreateListeningExperimentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	wordlist, err := h.wordlistRepo.GetByID(req.WordlistID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "wordlist not found"})
		return
	}

	// 候选条目集：词表全部条目的 id
	candidates := make([]int64, len(wordlist.Entries))
	inWordlist := make(map[int64]bool, len(wordlist.Entries))
	for i, e := range wordlist.Entries {
		candidates[i] = e.ID
		inWordlist[e.ID] = true
	}

	entryIDs := req.EntryIDs
	if len(entryIDs) > 0 {
		// 显式挑条目：必须在词表内，去重
		seen := map[int64]bool{}
		deduped := make([]int64, 0, len(entryIDs))
		for _, id := range entryIDs {
			if !inWordlist[id] {
				c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "entry not in wordlist", Detail: strconv.FormatInt(id, 10)})
				return
			}
			if !seen[id] {
				seen[id] = true
				deduped = append(deduped, id)
			}
		}
		entryIDs = deduped
	}

	seed := req.Seed
	if seed == 0 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to generate seed"})
			return
		}
		seed = int64(binary.LittleEndian.Uint64(b[:]) & 0x7fffffffffffffff)
		if seed == 0 {
			seed = 1
		}
	}

	if len(entryIDs) == 0 {
		entryIDs = services.PickEntries(candidates, req.PickCount, seed)
	}
	if len(entryIDs) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "no entries selected"})
		return
	}

	// 听辨人名单去重，保持给定先后（先后影响错序排位）
	listeners := make([]string, 0, len(req.Listeners))
	seenListener := map[string]bool{}
	for _, l := range req.Listeners {
		if l == "" || seenListener[l] {
			continue
		}
		seenListener[l] = true
		listeners = append(listeners, l)
	}
	if len(listeners) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "no valid listeners"})
		return
	}

	experiment := &models.ListeningExperiment{
		Name:             req.Name,
		DialectPointCode: req.DialectPointCode,
		WordlistID:       req.WordlistID,
		Seed:             seed,
		EntryIDs:         entryIDs,
		Listeners:        listeners,
	}
	if err := h.repo.CreateExperiment(experiment); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to create experiment", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusCreated, models.APIResponse{Code: 201, Message: "experiment created", Data: experiment})
}

func (h *ListeningHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid id"})
		return
	}

	experiment, err := h.repo.GetExperimentByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: experiment})
}

func (h *ListeningHandler) List(c *gin.Context) {
	var p models.Pagination
	if err := c.ShouldBindQuery(&p); err != nil {
		p = models.Pagination{}
	}
	p.Normalize()

	experiments, total, err := h.repo.ListExperiments(p.Offset, p.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list experiments"})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": experiments, "total": total, "offset": p.Offset, "limit": p.Limit}})
}

// Dispatch 下发首轮试次。编排由 (seed, entry_ids, listeners) 确定性导出，
// 插入靠唯一约束幂等——断过之后再次调用只补上缺的，已发已答的不会重来。
func (h *ListeningHandler) Dispatch(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}
	if experiment.Status == "closed" {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment closed"})
		return
	}

	orders := services.BuildListenerOrders(experiment.EntryIDs, len(experiment.Listeners), experiment.Seed)
	trials := make([]*models.ListeningTrial, 0, len(experiment.EntryIDs)*len(experiment.Listeners))
	for i, listener := range experiment.Listeners {
		for pos, entryID := range orders[i] {
			trials = append(trials, &models.ListeningTrial{
				ExperimentID: experiment.ID,
				Round:        1,
				Listener:     listener,
				EntryID:      entryID,
				Position:     pos + 1,
			})
		}
	}

	inserted, err := h.repo.InsertTrialsBatch(trials)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to dispatch trials", Detail: err.Error()})
		return
	}

	if experiment.Status == "draft" {
		if err := h.repo.UpdateExperimentStatus(experiment.ID, "dispatched"); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to update experiment status"})
			return
		}
		experiment.Status = "dispatched"
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "trials dispatched", Data: gin.H{
		"experiment_id": experiment.ID,
		"round":         1,
		"inserted":      inserted,
		"skipped":       len(trials) - inserted,
		"status":        experiment.Status,
	}})
}

// ListTrials 试次清单（听辨人拿到的题单），可按 listener / status 过滤。
func (h *ListeningHandler) ListTrials(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}

	var q models.ListeningTrialQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		q = models.ListeningTrialQuery{}
	}

	trials, err := h.repo.ListTrials(experiment.ID, q.Listener, q.Status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list trials"})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": trials, "total": len(trials)}})
}

// SubmitResponses 听辨人批量交作答。同一人对同一条只留第一次：
// 重复提交在唯一约束上并掉，返回 merged=true 与已存的第一次作答。
func (h *ListeningHandler) SubmitResponses(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}
	if experiment.Status == "closed" {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment closed"})
		return
	}

	listener := c.GetHeader("X-Listener")
	if listener == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "missing X-Listener header"})
		return
	}
	if experiment.ListenerIndex(listener) < 0 {
		c.JSON(http.StatusForbidden, models.ErrorResponse{Code: 403, Message: "listener not in experiment"})
		return
	}

	var req models.SubmitListeningResponsesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	results := make([]gin.H, 0, len(req.Answers))
	recorded, merged, rejected := 0, 0, 0
	for _, ans := range req.Answers {
		trial, err := h.repo.GetTrialByID(ans.TrialID)
		if err != nil || trial.ExperimentID != experiment.ID {
			results = append(results, gin.H{"trial_id": ans.TrialID, "result": "rejected", "reason": "trial not found"})
			rejected++
			continue
		}
		if trial.Listener != listener {
			results = append(results, gin.H{"trial_id": ans.TrialID, "result": "rejected", "reason": "trial belongs to another listener"})
			rejected++
			continue
		}
		if trial.Status == "voided" {
			results = append(results, gin.H{"trial_id": ans.TrialID, "result": "rejected", "reason": "trial voided"})
			rejected++
			continue
		}

		resp := &models.ListeningResponse{
			ExperimentID: experiment.ID,
			TrialID:      trial.ID,
			Listener:     listener,
			EntryID:      trial.EntryID,
			Choice:       ans.Choice,
			Note:         ans.Note,
		}
		inserted, err := h.repo.InsertResponseFirst(resp)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to save response", Detail: err.Error()})
			return
		}

		if !inserted {
			// 重复提交：并掉，返回已存的第一次作答
			existing, err := h.repo.GetResponse(experiment.ID, listener, trial.EntryID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to load existing response", Detail: err.Error()})
				return
			}
			results = append(results, gin.H{"trial_id": ans.TrialID, "entry_id": trial.EntryID, "result": "merged", "response": existing})
			merged++
			continue
		}

		if err := h.repo.MarkAnsweredByListenerEntry(experiment.ID, listener, trial.EntryID); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to mark trial answered", Detail: err.Error()})
			return
		}
		results = append(results, gin.H{"trial_id": ans.TrialID, "entry_id": trial.EntryID, "result": "recorded", "response": resp})
		recorded++
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "responses processed", Data: gin.H{
		"recorded": recorded,
		"merged":   merged,
		"rejected": rejected,
		"results":  results,
	}})
}

// ListResponses 已收作答清单，可按 listener 过滤。
func (h *ListeningHandler) ListResponses(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}

	responses, err := h.repo.ListResponses(experiment.ID, c.Query("listener"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list responses"})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": responses, "total": len(responses)}})
}

// Progress 进度：每个听辨人答了多少、还缺哪几条；整体谁还没交齐。
func (h *ListeningHandler) Progress(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}

	trials, err := h.repo.ListTrials(experiment.ID, "", "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to load trials"})
		return
	}

	type listenerProgress struct {
		Listener      string  `json:"listener"`
		Pending       []int64 `json:"pending_entry_ids"`
		PendingCount  int     `json:"pending"`
		AnsweredCount int     `json:"answered"`
		VoidedCount   int     `json:"voided"`
		Done          bool    `json:"done"`
	}
	byListener := make(map[string]*listenerProgress, len(experiment.Listeners))
	order := make([]string, 0, len(experiment.Listeners))
	for _, l := range experiment.Listeners {
		byListener[l] = &listenerProgress{Listener: l, Pending: []int64{}}
		order = append(order, l)
	}
	for _, t := range trials {
		lp, ok := byListener[t.Listener]
		if !ok {
			continue
		}
		switch t.Status {
		case "pending":
			lp.Pending = append(lp.Pending, t.EntryID)
			lp.PendingCount++
		case "answered":
			lp.AnsweredCount++
		case "voided":
			lp.VoidedCount++
		}
	}

	items := make([]*listenerProgress, 0, len(order))
	incomplete := []string{}
	for _, l := range order {
		lp := byListener[l]
		lp.Done = lp.PendingCount == 0
		if !lp.Done {
			incomplete = append(incomplete, l)
		}
		items = append(items, lp)
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{
		"experiment_id":       experiment.ID,
		"status":              experiment.Status,
		"listeners":           items,
		"incomplete_listener": incomplete,
		"all_done":            len(incomplete) == 0,
	}})
}

// Void 作废一批进行中的试次（已作答的不动）。作废造成的缺口由 Redispatch 补位。
func (h *ListeningHandler) Void(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}

	var req models.VoidListeningTrialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	voided, err := h.repo.VoidTrials(experiment.ID, req.TrialIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to void trials", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "trials voided", Data: gin.H{
		"requested": len(req.TrialIDs),
		"voided":    voided,
	}})
}

// Redispatch 重排补位：把已作废且未补发的条目（外加指定的补位条目）
// 编进新一轮，按 (seed, round, 听辨人序号) 重新错序后下发。
func (h *ListeningHandler) Redispatch(c *gin.Context) {
	experiment, ok := h.loadExperiment(c)
	if !ok {
		return
	}
	if experiment.Status == "closed" {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment closed"})
		return
	}

	var req models.RedispatchListeningRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 允许空 body：只补作废缺口
		req = models.RedispatchListeningRequest{}
	}

	owed, err := h.repo.OwedEntries(experiment.ID, req.Listener)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to find owed entries", Detail: err.Error()})
		return
	}

	// 额外补位条目：并入实验条目集，分发给指定听辨人（缺省为全体）
	if len(req.EntryIDs) > 0 {
		targets := experiment.Listeners
		if req.Listener != "" {
			if experiment.ListenerIndex(req.Listener) < 0 {
				c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "listener not in experiment"})
				return
			}
			targets = []string{req.Listener}
		}
		for _, l := range targets {
			pending, err := h.repo.PendingEntryIDs(experiment.ID, l)
			if err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to check pending entries", Detail: err.Error()})
				return
			}
			for _, id := range req.EntryIDs {
				if pending[id] {
					continue // 手上已有这条待答，不重复发
				}
				owed[l] = append(owed[l], id)
			}
		}
		if err := h.repo.AddEntryIDs(experiment.ID, req.EntryIDs); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to merge extra entries", Detail: err.Error()})
			return
		}
	}

	if len(owed) == 0 {
		c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "nothing to redispatch", Data: gin.H{"inserted": 0}})
		return
	}

	round, err := h.repo.MaxRound(experiment.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to get current round", Detail: err.Error()})
		return
	}
	round++

	trials := make([]*models.ListeningTrial, 0)
	for listener, entryIDs := range owed {
		// 去重（作废缺口与额外补位可能撞车）
		seen := map[int64]bool{}
		unique := make([]int64, 0, len(entryIDs))
		for _, id := range entryIDs {
			if !seen[id] {
				seen[id] = true
				unique = append(unique, id)
			}
		}
		shuffled := services.ShuffleForRedispatch(unique, experiment.Seed, round, experiment.ListenerIndex(listener))
		for pos, entryID := range shuffled {
			trials = append(trials, &models.ListeningTrial{
				ExperimentID: experiment.ID,
				Round:        round,
				Listener:     listener,
				EntryID:      entryID,
				Position:     pos + 1,
			})
		}
	}

	inserted, err := h.repo.InsertTrialsBatch(trials)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to redispatch", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "trials redispatched", Data: gin.H{
		"experiment_id": experiment.ID,
		"round":         round,
		"inserted":      inserted,
		"listeners":     len(owed),
	}})
}

func (h *ListeningHandler) loadExperiment(c *gin.Context) (*models.ListeningExperiment, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid id"})
		return nil, false
	}
	experiment, err := h.repo.GetExperimentByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
		return nil, false
	}
	return experiment, true
}
