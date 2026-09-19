package handlers

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"cc-053/internal/models"
	"cc-053/internal/repository"
)

type ListeningHandler struct {
	repo *repository.ListeningRepo
}

func NewListeningHandler(repo *repository.ListeningRepo) *ListeningHandler {
	return &ListeningHandler{repo: repo}
}

// listenerID 从请求头取听辨人（平台内以 speaker.id 为听辨人身份）。
func listenerID(c *gin.Context) (int64, bool) {
	raw := c.GetHeader("X-Listener-ID")
	if raw == "" {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{Code: 401, Message: "missing X-Listener-ID header"})
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid X-Listener-ID header"})
		return 0, false
	}
	return id, true
}

func experimentIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid experiment id"})
		return 0, false
	}
	return id, true
}

func dedupePositiveIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Create 创建实验并编排首批试次。
func (h *ListeningHandler) Create(c *gin.Context) {
	var req models.CreateListeningExperimentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	listeners := dedupePositiveIDs(req.ListenerIDs)
	if len(listeners) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "listener_ids must contain at least one valid id"})
		return
	}
	missingListeners, err := h.repo.CheckListenersExist(listeners)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to validate listeners", Detail: err.Error()})
		return
	}
	if len(missingListeners) > 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "some listeners do not exist", Detail: "missing listener ids in speakers table"})
		return
	}

	// 挑条目：显式给 EntryIDs 用显式的（必须校验音频存在）；否则自动挑该调查点下有完成音频的条目。
	entryIDs := dedupePositiveIDs(req.EntryIDs)
	if len(entryIDs) > 0 {
		missingAudio, err := h.repo.EntriesMissingAudio(req.DialectPointCode, req.WordlistID, entryIDs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to validate entry audio", Detail: err.Error()})
			return
		}
		if len(missingAudio) > 0 {
			c.JSON(http.StatusUnprocessableEntity, models.ErrorResponse{
				Code: 422, Message: "some entries have no completed audio segment at this dialect point",
			})
			return
		}
	} else {
		entryIDs, err = h.repo.DiscoverAvailableEntries(req.DialectPointCode, req.WordlistID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to discover entries", Detail: err.Error()})
			return
		}
		if len(entryIDs) == 0 {
			c.JSON(http.StatusUnprocessableEntity, models.ErrorResponse{
				Code: 422, Message: "no completed audio segments found for this dialect point / wordlist",
			})
			return
		}
	}

	seed := req.Seed
	if seed == 0 {
		u := uuid.New()
		seed = int64(binary.BigEndian.Uint64(u[0:8]))
	}

	exp := &models.ListeningExperiment{
		Name:             req.Name,
		DialectPointCode: req.DialectPointCode,
		WordlistID:       req.WordlistID,
		EntryIDs:         entryIDs,
		ListenerIDs:      listeners,
		Seed:             seed,
	}
	if err := h.repo.CreateExperiment(exp); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to create experiment", Detail: err.Error()})
		return
	}

	// 创建即编排首批（batch_size=0 表示一次排完）。
	batch, err := h.repo.IssueBatch(exp.ID, req.BatchSize)
	if err != nil {
		// 实验已建但发批失败：回传实验 id，可用续发接口重试，已建数据不丢。
		c.JSON(http.StatusUnprocessableEntity, models.APIResponse{
			Code:    422,
			Message: "experiment created but initial batch failed: " + err.Error(),
			Data:    gin.H{"experiment_id": exp.ID},
		})
		return
	}

	c.JSON(http.StatusCreated, models.APIResponse{
		Code: 201, Message: "listening experiment created",
		Data: gin.H{"experiment": exp, "first_batch": batch},
	})
}

// GetByID 实验详情（含条目、听辨人、编排种子）。
func (h *ListeningHandler) GetByID(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	exp, err := h.repo.GetExperiment(id)
	if errors.Is(err, repository.ErrListeningNotFound) {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to get experiment", Detail: err.Error()})
		return
	}
	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: exp})
}

func (h *ListeningHandler) List(c *gin.Context) {
	var p models.Pagination
	_ = c.ShouldBindQuery(&p)
	p.Normalize()
	items, total, err := h.repo.ListExperiments(p.Offset, p.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list experiments"})
		return
	}
	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": items, "total": total, "offset": p.Offset, "limit": p.Limit}})
}

// Trials 试次编排明细（位置、状态、是否补位）。
func (h *ListeningHandler) Trials(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	trials, err := h.repo.ListTrials(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list trials", Detail: err.Error()})
		return
	}
	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": trials}})
}

// Progress 缺谁、缺哪几条一目了然。
func (h *ListeningHandler) Progress(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	progress, err := h.repo.GetProgress(id)
	if errors.Is(err, repository.ErrListeningNotFound) || errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to get progress", Detail: err.Error()})
		return
	}
	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: progress})
}

// IssueBatch 断点续发：从中断位置之后接着发，不重发已收的。
func (h *ListeningHandler) IssueBatch(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	var req models.IssueListeningBatchRequest
	_ = c.ShouldBindJSON(&req) // body 可空：空 body 发完剩余

	batch, err := h.repo.IssueBatch(id, req.BatchSize)
	switch {
	case errors.Is(err, repository.ErrListeningNotFound):
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
	case errors.Is(err, repository.ErrListeningNotActive):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment is not active"})
	case errors.Is(err, repository.ErrListeningNothingToIssue):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "no remaining entries to issue; all entries already arranged"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to issue batch", Detail: err.Error()})
	default:
		c.JSON(http.StatusCreated, models.APIResponse{Code: 201, Message: "batch issued", Data: batch})
	}
}

// Queue 听辨人拉自己的作答队列（按个人收听次序，已错开）。
func (h *ListeningHandler) Queue(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	lid, ok := listenerID(c)
	if !ok {
		return
	}
	items, err := h.repo.GetQueue(id, lid, c.Query("status"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to get queue", Detail: err.Error()})
		return
	}
	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": items}})
}

// Respond 听辨人交作答；同一人对同一条重复提交只留第一次。
func (h *ListeningHandler) Respond(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	lid, ok := listenerID(c)
	if !ok {
		return
	}
	var req models.ListeningResponseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}
	if req.Confidence < 0 || req.Confidence > 5 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "confidence must be between 0 and 5"})
		return
	}

	resp, duplicated, err := h.repo.SubmitResponse(id, lid, req)
	switch {
	case errors.Is(err, repository.ErrListeningNotFound):
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
	case errors.Is(err, repository.ErrListeningNotActive):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment is not active"})
	case errors.Is(err, repository.ErrAssignmentNotFound):
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "this trial was not assigned to you"})
	case errors.Is(err, repository.ErrTrialVoided):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "trial has been voided; a replacement will be issued"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to submit response", Detail: err.Error()})
	default:
		status := http.StatusCreated
		message := "response recorded"
		if duplicated {
			status = http.StatusOK
			message = "duplicate submission merged; only the first response is kept"
		}
		c.JSON(status, models.APIResponse{Code: status, Message: message, Data: resp})
	}
}

// VoidTrial 中途作废一条试次，可同时重排补位。
func (h *ListeningHandler) VoidTrial(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	trialID, err := strconv.ParseInt(c.Param("trialId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid trial id"})
		return
	}
	var req models.VoidTrialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	res, err := h.repo.VoidTrial(id, trialID, req.Reason, req.Replace)
	switch {
	case errors.Is(err, repository.ErrTrialNotFound):
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "trial not found in this experiment"})
	case errors.Is(err, repository.ErrTrialAlreadyVoided):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "trial already voided"})
	case errors.Is(err, repository.ErrListeningNotActive):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment is not active"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to void trial", Detail: err.Error()})
	default:
		c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "trial voided" + replacementMsg(res), Data: res})
	}
}

func replacementMsg(r *repository.ReplacementResult) string {
	if r.ReplacementTrialID != 0 {
		return "; replacement trial issued"
	}
	if len(r.UncoveredListenerIDs) > 0 {
		return "; no alternative audio available yet, listeners left uncovered"
	}
	return ""
}

// ReplaceTrial 对已作废但尚未补位（或补位失败）的试次单独重排补位。
func (h *ListeningHandler) ReplaceTrial(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	trialID, err := strconv.ParseInt(c.Param("trialId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid trial id"})
		return
	}
	res, err := h.repo.ReplaceTrial(id, trialID)
	switch {
	case errors.Is(err, repository.ErrTrialNotFound):
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "trial not found in this experiment"})
	case errors.Is(err, repository.ErrTrialNotVoided):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "trial is not voided; void it before requesting replacement"})
	case errors.Is(err, repository.ErrTrialAlreadyReplaced):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "voided trial already has an active replacement"})
	case errors.Is(err, repository.ErrListeningNotActive):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "experiment is not active"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to replace trial", Detail: err.Error()})
	default:
		c.JSON(http.StatusCreated, models.APIResponse{Code: 201, Message: "replacement issued" + replacementMsg(res), Data: res})
	}
}

// Complete 收齐后收尾冻结；未交齐或作废未补位时 409 并在 detail 给出缺口。
func (h *ListeningHandler) Complete(c *gin.Context) {
	id, ok := experimentIDParam(c)
	if !ok {
		return
	}
	progress, err := h.repo.CompleteExperiment(id)
	switch {
	case errors.Is(err, repository.ErrListeningNotFound):
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "experiment not found"})
	case errors.Is(err, repository.ErrListeningHasPending), errors.Is(err, repository.ErrListeningUncovered):
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "cannot complete: responses still missing or voided trials uncovered", Detail: err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to complete experiment", Detail: err.Error()})
	default:
		c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "experiment completed and frozen", Data: progress})
	}
}
