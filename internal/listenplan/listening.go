// Package listenplan 放听辨实验的纯编排算法：与数据库无关、确定性、可复现，可独立单测。
package listenplan

import (
	"math/rand"
	"sort"
)

// ListenerRank 听辨人在实验内的固定序号：决定 Latin-square 轮换偏移。
type ListenerRank struct {
	ID   int64
	Rank int // 0 起
}

// PlannedTrial 编排产出的一条试次（尚未落库）。
type PlannedTrial struct {
	EntryID  int64
	Position int // 实验内全局位置，1 起，只增不减
	Slot     int // 本批内的槽位，0 起
}

// PlannedAssignment 编排产出的一条分发。
type PlannedAssignment struct {
	ListenerID int64
	Slot       int // 对应批内哪个试次槽位
	OrderIndex int // 该听辨人自己的收听次序
	Offset     int // 轮换偏移
}

// BatchPlan 一批的编排结果。
type BatchPlan struct {
	Trials      []PlannedTrial
	Assignments [][]PlannedAssignment // 与 Trials 按槽位对齐
}

// ShuffleEntries 以实验 seed 做确定性的 Fisher-Yates 打乱。
// 规范序（升序）入、打乱序出；同一 (entries, seed) 永远得到同一结果，可复现、可审计。
func ShuffleEntries(entries []int64, seed int64) []int64 {
	out := append([]int64(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	rng := rand.New(rand.NewSource(seed))
	for i := len(out) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// PlanBatch 对一批条目做轮换编排（cyclic Latin square）。
//
// batchEntries: 本批条目（顺序即全局位置顺序）；
// startPosition: 本批第一个槽位的全局位置（1 起，只增不减）；
// ranks: 参与本批的听辨人及其固定序号；
// baseOrder: 每位听辨人此前已拿到的试次数（断点续发/补位时各人可能不同），
//
//	其个人次序从 baseOrder+1 继续编号。
//
// rank=k 的听辨人在本批听到的第 j 条（j 从 0 起）是槽位 (j+k) mod b；
// 等价地，槽位 s 的试次发给 rank=k 时其个人次序为 (s-k) mod b。
// 于是同一条目在 rank 不同的听辨人手里必然落在不同位置（仅当 k 模 b 相同才重合；
// 听众多于槽位数时物理上无法完全错开，但不会"总在同一位置"）。
func PlanBatch(batchEntries []int64, startPosition int, ranks []ListenerRank, baseOrder map[int64]int) *BatchPlan {
	b := len(batchEntries)
	plan := &BatchPlan{
		Trials:      make([]PlannedTrial, b),
		Assignments: make([][]PlannedAssignment, b),
	}
	for s, e := range batchEntries {
		plan.Trials[s] = PlannedTrial{EntryID: e, Position: startPosition + s, Slot: s}
	}
	for _, lr := range ranks {
		k := lr.Rank
		base := 0
		if baseOrder != nil {
			base = baseOrder[lr.ID]
		}
		for s := 0; s < b; s++ {
			rel := (s - k) % b
			if rel < 0 {
				rel += b
			}
			plan.Assignments[s] = append(plan.Assignments[s], PlannedAssignment{
				ListenerID: lr.ID,
				Slot:       s,
				OrderIndex: base + rel + 1,
				Offset:     k,
			})
		}
	}
	return plan
}
