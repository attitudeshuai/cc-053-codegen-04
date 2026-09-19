package services

import (
	"math/rand"
	"sort"
)

// 听辨实验的编排全部从这里派生：同一个 (seed, 条目集, 听辨人数) 永远得到同一份次序，
// 因此断点续发可以安全重放，已发出去的试次不会被重来一遍。

// deriveSeed 由基础种子与若干分量混合出子种子（boost hash_combine 风格，纯算术、跨运行确定）。
func deriveSeed(base int64, parts ...int64) int64 {
	h := uint64(base)
	for _, p := range parts {
		h ^= uint64(p) + 0x9e3779b97f4a7c15 + (h << 6) + (h >> 2)
	}
	return int64(h)
}

// ShuffleEntries 用给定种子对条目做确定性 Fisher-Yates 打乱，返回新切片。
func ShuffleEntries(entries []int64, seed int64) []int64 {
	out := make([]int64, len(entries))
	copy(out, entries)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// PickEntries 从候选条目中按种子抽 count 条；count<=0 或超过候选数时取全部。
// 返回升序切片，保证落库的条目集稳定。
func PickEntries(candidates []int64, count int, seed int64) []int64 {
	picked := ShuffleEntries(candidates, seed)
	if count > 0 && count < len(picked) {
		picked = picked[:count]
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i] < picked[j] })
	return picked
}

// BuildListenerOrders 为每个听辨人生成错开的条目次序。
// 先按实验种子打乱出基础序列，第 i 个听辨人再循环移位 i*step 位
// （step = max(1, N/M)），因此同一条目在不同人手中的位置两两不同
// （听辨人数不超过条目数时严格成立；超过时取模回绕，无法避免）。
func BuildListenerOrders(entries []int64, listenerCount int, seed int64) [][]int64 {
	n := len(entries)
	orders := make([][]int64, listenerCount)
	if n == 0 || listenerCount <= 0 {
		return orders
	}
	base := ShuffleEntries(entries, seed)
	step := n / listenerCount
	if step < 1 {
		step = 1
	}
	for i := 0; i < listenerCount; i++ {
		shift := (i * step) % n
		order := make([]int64, n)
		for j := 0; j < n; j++ {
			order[j] = base[(j+shift)%n]
		}
		orders[i] = order
	}
	return orders
}

// ShuffleForRedispatch 重排补位时的次序：由 (实验种子, 轮次, 听辨人序号) 派生子种子，
// 与首轮及其它听辨人的序列都错开，同时保持可重放。
func ShuffleForRedispatch(entries []int64, seed int64, round int, listenerIndex int) []int64 {
	return ShuffleEntries(entries, deriveSeed(seed, int64(round), int64(listenerIndex)))
}
