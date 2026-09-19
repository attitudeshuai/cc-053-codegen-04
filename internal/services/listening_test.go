package services

import (
	"reflect"
	"testing"
)

func TestShuffleEntriesDeterministic(t *testing.T) {
	entries := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	a := ShuffleEntries(entries, 42)
	b := ShuffleEntries(entries, 42)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed should give same order: %v vs %v", a, b)
	}
	// 原切片不被改动
	if !reflect.DeepEqual(entries, []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Fatalf("input slice mutated: %v", entries)
	}
	// 打乱后是同一集合
	if !sameSet(a, entries) {
		t.Fatalf("shuffled result is not a permutation: %v", a)
	}
}

func TestBuildListenerOrdersStaggered(t *testing.T) {
	entries := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	const listeners = 4
	orders := BuildListenerOrders(entries, listeners, 7)
	if len(orders) != listeners {
		t.Fatalf("expected %d orders, got %d", listeners, len(orders))
	}

	// 每个人都是完整集合的一个排列
	for i, o := range orders {
		if !sameSet(o, entries) {
			t.Fatalf("listener %d order is not a permutation: %v", i, o)
		}
	}

	// 关键性质：同一条目在不同人手中的位置两两不同（人数 <= 条目数时）
	for _, entry := range entries {
		seen := map[int]bool{}
		for _, o := range orders {
			pos := indexOf(o, entry)
			if seen[pos] {
				t.Fatalf("entry %d lands on position %d for more than one listener", entry, pos)
			}
			seen[pos] = true
		}
	}

	// 可重放：同种子再算一遍完全一致
	again := BuildListenerOrders(entries, listeners, 7)
	for i := range orders {
		if !reflect.DeepEqual(orders[i], again[i]) {
			t.Fatalf("listener %d order not reproducible", i)
		}
	}
}

func TestBuildListenerOrdersDifferentSeeds(t *testing.T) {
	entries := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	a := BuildListenerOrders(entries, 3, 1)
	b := BuildListenerOrders(entries, 3, 2)
	same := true
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds should produce different orders")
	}
}

func TestPickEntries(t *testing.T) {
	candidates := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// 抽样数量正确、是子集、可重放
	a := PickEntries(candidates, 4, 99)
	b := PickEntries(candidates, 4, 99)
	if len(a) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(a))
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed should give same pick: %v vs %v", a, b)
	}
	in := map[int64]bool{}
	for _, id := range candidates {
		in[id] = true
	}
	for _, id := range a {
		if !in[id] {
			t.Fatalf("picked entry %d not in candidates", id)
		}
	}

	// count<=0 或超界时取全部
	all := PickEntries(candidates, 0, 99)
	if len(all) != len(candidates) {
		t.Fatalf("expected all entries, got %d", len(all))
	}
	over := PickEntries(candidates, 100, 99)
	if len(over) != len(candidates) {
		t.Fatalf("expected all entries when count overflows, got %d", len(over))
	}
}

func TestShuffleForRedispatch(t *testing.T) {
	entries := []int64{3, 5, 8}
	// 同参数可重放
	a := ShuffleForRedispatch(entries, 7, 2, 1)
	b := ShuffleForRedispatch(entries, 7, 2, 1)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("redispatch shuffle not reproducible: %v vs %v", a, b)
	}
	if !sameSet(a, entries) {
		t.Fatalf("redispatch result is not a permutation: %v", a)
	}
	// 不同听辨人/轮次的序列应不同（集合小，偶发相同概率低；多试几组）
	diff := false
	for round := 1; round <= 3 && !diff; round++ {
		for li := 0; li < 3 && !diff; li++ {
			if !reflect.DeepEqual(ShuffleForRedispatch(entries, 7, round, li), a) {
				diff = true
			}
		}
	}
	if !diff {
		t.Fatal("redispatch shuffle should vary by round/listener")
	}
}

func sameSet(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	cnt := map[int64]int{}
	for _, x := range a {
		cnt[x]++
	}
	for _, x := range b {
		cnt[x]--
		if cnt[x] < 0 {
			return false
		}
	}
	return true
}

func indexOf(s []int64, v int64) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
