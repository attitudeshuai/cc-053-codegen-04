package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/gin-gonic/gin"

	"cc-053/internal/database"
	"cc-053/internal/repository"
)

// 集成测试：起真实 PostgreSQL（embedded-postgres 下载便携二进制），
// 走 HTTP 层验证听辨实验全链路。默认跳过，LISTENING_INTEGRATION=1 时运行。
func setupListeningIntegration(t *testing.T) (*gin.Engine, *sql.DB, func()) {
	t.Helper()
	if os.Getenv("LISTENING_INTEGRATION") == "" {
		t.Skip("set LISTENING_INTEGRATION=1 to run")
	}

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V14).
		Port(15433).
		Database("listening_test").
		Username("test").
		Password("test"))
	if err := pg.Start(); err != nil {
		t.Fatalf("start embedded postgres: %v", err)
	}

	dsn := "host=localhost port=15433 user=test password=test dbname=listening_test sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		pg.Stop()
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(db); err != nil {
		pg.Stop()
		t.Fatalf("migrations: %v", err)
	}

	wordlistRepo := repository.NewWordlistRepo(db)
	listeningRepo := repository.NewListeningRepo(db)
	handler := NewListeningHandler(listeningRepo, wordlistRepo)
	wordlistHandler := NewWordlistHandler(wordlistRepo)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/wordlists", wordlistHandler.Create)
	r.POST("/api/v1/listening/experiments", handler.Create)
	r.GET("/api/v1/listening/experiments/:id", handler.GetByID)
	r.POST("/api/v1/listening/experiments/:id/dispatch", handler.Dispatch)
	r.GET("/api/v1/listening/experiments/:id/trials", handler.ListTrials)
	r.POST("/api/v1/listening/experiments/:id/responses", handler.SubmitResponses)
	r.GET("/api/v1/listening/experiments/:id/responses", handler.ListResponses)
	r.GET("/api/v1/listening/experiments/:id/progress", handler.Progress)
	r.POST("/api/v1/listening/experiments/:id/void", handler.Void)
	r.POST("/api/v1/listening/experiments/:id/redispatch", handler.Redispatch)

	cleanup := func() {
		db.Close()
		pg.Stop()
	}
	return r, db, cleanup
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body interface{}, headers map[string]string) (int, map[string]interface{}) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var parsed map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("%s %s: response not JSON: %s", method, path, w.Body.String())
	}
	return w.Code, parsed
}

func dataOf(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no object data: %v", resp)
	}
	return data
}

func TestListeningExperimentLifecycle(t *testing.T) {
	r, db, cleanup := setupListeningIntegration(t)
	defer cleanup()

	// 1. 建词表（10 个条目）
	entries := []map[string]interface{}{}
	for i := 1; i <= 10; i++ {
		entries = append(entries, map[string]interface{}{
			"id": i, "hanzi": fmt.Sprintf("字%d", i), "gloss": fmt.Sprintf("gloss%d", i),
		})
	}
	code, resp := doJSON(t, r, "POST", "/api/v1/wordlists", map[string]interface{}{
		"name": "测试词表", "entries": entries,
	}, nil)
	if code != http.StatusCreated {
		t.Fatalf("create wordlist: got %d: %v", code, resp)
	}
	wordlistID := int64(dataOf(t, resp)["id"].(float64))

	// 2. 编排实验：3 个听辨人，固定种子
	code, resp = doJSON(t, r, "POST", "/api/v1/listening/experiments", map[string]interface{}{
		"name": "实验一", "dialect_point_code": "DP01", "wordlist_id": wordlistID,
		"listeners": []string{"alice", "bob", "carol"}, "seed": 12345,
	}, nil)
	if code != http.StatusCreated {
		t.Fatalf("create experiment: got %d: %v", code, resp)
	}
	expData := dataOf(t, resp)
	expID := int64(expData["id"].(float64))
	if got := len(expData["entry_ids"].([]interface{})); got != 10 {
		t.Fatalf("expected 10 entries, got %d", got)
	}
	base := fmt.Sprintf("/api/v1/listening/experiments/%d", expID)

	// 3. 下发：30 个试次全插入；再发一次全部跳过（断点续发不重来）
	code, resp = doJSON(t, r, "POST", base+"/dispatch", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("dispatch: got %d: %v", code, resp)
	}
	if got := int(dataOf(t, resp)["inserted"].(float64)); got != 30 {
		t.Fatalf("first dispatch: expected 30 inserted, got %d", got)
	}
	code, resp = doJSON(t, r, "POST", base+"/dispatch", nil, nil)
	if got := int(dataOf(t, resp)["inserted"].(float64)); got != 0 {
		t.Fatalf("re-dispatch should insert 0, got %d", got)
	}

	// 4. 错序校验：同一条目在三人手中的位置两两不同
	_, resp = doJSON(t, r, "GET", base+"/trials", nil, nil)
	trials := dataOf(t, resp)["items"].([]interface{})
	posByEntry := map[int64]map[string]int{}
	for _, item := range trials {
		tr := item.(map[string]interface{})
		entry := int64(tr["entry_id"].(float64))
		listener := tr["listener"].(string)
		pos := int(tr["position"].(float64))
		if posByEntry[entry] == nil {
			posByEntry[entry] = map[string]int{}
		}
		posByEntry[entry][listener] = pos
	}
	for entry, positions := range posByEntry {
		seen := map[int]bool{}
		for _, p := range positions {
			if seen[p] {
				t.Fatalf("entry %d: position %d shared by multiple listeners", entry, p)
			}
			seen[p] = true
		}
	}

	// 找到 alice 的前两个试次
	_, resp = doJSON(t, r, "GET", base+"/trials?listener=alice", nil, nil)
	aliceTrials := dataOf(t, resp)["items"].([]interface{})
	if len(aliceTrials) != 10 {
		t.Fatalf("alice should have 10 trials, got %d", len(aliceTrials))
	}
	trial1 := aliceTrials[0].(map[string]interface{})
	trial1ID := int64(trial1["id"].(float64))

	// 5. alice 交作答：第一次 recorded；重复交同一条 → merged，库里仍只有一条
	alice := map[string]string{"X-Listener": "alice"}
	code, resp = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
		"answers": []map[string]interface{}{{"trial_id": trial1ID, "choice": "A"}},
	}, alice)
	if code != http.StatusOK || int(dataOf(t, resp)["recorded"].(float64)) != 1 {
		t.Fatalf("first submit should record 1: %d %v", code, resp)
	}
	code, resp = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
		"answers": []map[string]interface{}{{"trial_id": trial1ID, "choice": "B"}},
	}, alice)
	if got := int(dataOf(t, resp)["merged"].(float64)); got != 1 {
		t.Fatalf("duplicate submit should merge: %v", resp)
	}
	// 并掉后保留的是第一次的 choice=A
	results := dataOf(t, resp)["results"].([]interface{})
	merged := results[0].(map[string]interface{})
	mergedResp := merged["response"].(map[string]interface{})
	if mergedResp["choice"].(string) != "A" {
		t.Fatalf("merged response should keep first choice A, got %v", mergedResp["choice"])
	}
	var respCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM listening_responses WHERE experiment_id=$1 AND listener='alice'`, expID).Scan(&respCount); err != nil {
		t.Fatal(err)
	}
	if respCount != 1 {
		t.Fatalf("expected 1 response row after merge, got %d", respCount)
	}

	// 6. 越权/错人提交：bob 拿 alice 的试次交 → rejected
	code, resp = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
		"answers": []map[string]interface{}{{"trial_id": trial1ID, "choice": "C"}},
	}, map[string]string{"X-Listener": "bob"})
	if got := int(dataOf(t, resp)["rejected"].(float64)); got != 1 {
		t.Fatalf("bob answering alice's trial should be rejected: %v", resp)
	}

	// 7. 进度：alice 缺 9 条，bob/carol 各缺 10 条
	_, resp = doJSON(t, r, "GET", base+"/progress", nil, nil)
	progress := dataOf(t, resp)
	if progress["all_done"].(bool) {
		t.Fatal("should not be all done")
	}
	for _, item := range progress["listeners"].([]interface{}) {
		lp := item.(map[string]interface{})
		switch lp["listener"].(string) {
		case "alice":
			if int(lp["pending"].(float64)) != 9 || int(lp["answered"].(float64)) != 1 {
				t.Fatalf("alice progress wrong: %v", lp)
			}
		case "bob":
			if int(lp["pending"].(float64)) != 10 {
				t.Fatalf("bob progress wrong: %v", lp)
			}
		}
	}

	// 8. 作废 alice 第二个试次 → 重排补位 → 新一轮补回该条目
	trial2 := aliceTrials[1].(map[string]interface{})
	trial2ID := int64(trial2["id"].(float64))
	trial2Entry := int64(trial2["entry_id"].(float64))
	code, resp = doJSON(t, r, "POST", base+"/void", map[string]interface{}{
		"trial_ids": []int64{trial2ID},
	}, nil)
	if int(dataOf(t, resp)["voided"].(float64)) != 1 {
		t.Fatalf("void should affect 1: %v", resp)
	}
	code, resp = doJSON(t, r, "POST", base+"/redispatch", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("redispatch: %v", resp)
	}
	rd := dataOf(t, resp)
	if int(rd["inserted"].(float64)) != 1 || int(rd["round"].(float64)) != 2 {
		t.Fatalf("redispatch should insert 1 trial in round 2: %v", rd)
	}

	// 补位的试次可正常作答
	_, resp = doJSON(t, r, "GET", base+"/trials?listener=alice&status=pending", nil, nil)
	pending := dataOf(t, resp)["items"].([]interface{})
	var refillID int64
	for _, item := range pending {
		tr := item.(map[string]interface{})
		if int64(tr["entry_id"].(float64)) == trial2Entry && int(tr["round"].(float64)) == 2 {
			refillID = int64(tr["id"].(float64))
		}
	}
	if refillID == 0 {
		t.Fatal("no round-2 refill trial for voided entry")
	}
	code, resp = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
		"answers": []map[string]interface{}{{"trial_id": refillID, "choice": "D"}},
	}, alice)
	if int(dataOf(t, resp)["recorded"].(float64)) != 1 {
		t.Fatalf("answering refilled trial should record: %v", resp)
	}

	// 9. 已作废的试次不能再作答
	code, resp = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
		"answers": []map[string]interface{}{{"trial_id": trial2ID, "choice": "E"}},
	}, alice)
	if int(dataOf(t, resp)["rejected"].(float64)) != 1 {
		t.Fatalf("voided trial should reject answers: %v", resp)
	}

	// 10. 全部答完后再次 dispatch：一个都不插（已收上来的不会重来）
	_, resp = doJSON(t, r, "GET", base+"/trials?listener=alice&status=pending", nil, nil)
	for _, item := range dataOf(t, resp)["items"].([]interface{}) {
		tr := item.(map[string]interface{})
		tid := int64(tr["id"].(float64))
		code, resp = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
			"answers": []map[string]interface{}{{"trial_id": tid, "choice": "X"}},
		}, alice)
		if int(dataOf(t, resp)["recorded"].(float64)) != 1 {
			t.Fatalf("answer trial %d: %v", tid, resp)
		}
	}
	code, resp = doJSON(t, r, "POST", base+"/dispatch", nil, nil)
	if got := int(dataOf(t, resp)["inserted"].(float64)); got != 0 {
		t.Fatalf("dispatch after completion should insert 0, got %d", got)
	}
	var answeredCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM listening_responses WHERE experiment_id=$1 AND listener='alice'`, expID).Scan(&answeredCount); err != nil {
		t.Fatal(err)
	}
	if answeredCount != 10 {
		t.Fatalf("alice should have exactly 10 responses, got %d", answeredCount)
	}

	// 11. 进度：alice 全部完成
	_, resp = doJSON(t, r, "GET", base+"/progress", nil, nil)
	for _, item := range dataOf(t, resp)["listeners"].([]interface{}) {
		lp := item.(map[string]interface{})
		if lp["listener"].(string) == "alice" && !lp["done"].(bool) {
			t.Fatalf("alice should be done: %v", lp)
		}
	}

	// 12. 作废已作答的试次不生效（voided=0）
	var aliceTrialID int64
	if err := db.QueryRow(`SELECT id FROM listening_trials WHERE experiment_id=$1 AND listener='alice' AND status='answered' LIMIT 1`, expID).Scan(&aliceTrialID); err != nil {
		t.Fatal(err)
	}
	_, resp = doJSON(t, r, "POST", base+"/void", map[string]interface{}{
		"trial_ids": []int64{aliceTrialID},
	}, nil)
	if got := int(dataOf(t, resp)["voided"].(float64)); got != 0 {
		t.Fatalf("voiding answered trial should affect 0, got %d", got)
	}

	// 13. 未登记的听辨人交作答 → 403
	code, _ = doJSON(t, r, "POST", base+"/responses", map[string]interface{}{
		"answers": []map[string]interface{}{{"trial_id": 1, "choice": "Z"}},
	}, map[string]string{"X-Listener": "mallory"})
	if code != http.StatusForbidden {
		t.Fatalf("unknown listener should get 403, got %d", code)
	}
}
