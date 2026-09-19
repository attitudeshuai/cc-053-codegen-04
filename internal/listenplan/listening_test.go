package listenplan

import (
	"sort"
	"testing"
)

func TestShuffleIsDeterministicAndPermutation(t *testing.T) {
	entries := []int64{10, 30, 20, 50, 40}
	a := ShuffleEntries(entries, 42)
	b := ShuffleEntries(entries, 42)
	if len(a) != len(entries) {
		t.Fatalf("length changed: %d vs %d", len(a), len(entries))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("shuffle not deterministic at %d: %d vs %d", i, a[i], b[i])
		}
	}
	want := append([]int64(nil), entries...)
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	got := append([]int64(nil), a...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("not a permutation: %v", a)
		}
	}
	c := ShuffleEntries(entries, 7)
	diff := false
	for i := range a {
		if a[i] != c[i] {
			diff = true
		}
	}
	if !diff {
		t.Fatalf("different seeds produced identical order: %v", a)
	}
}

func TestPlanBatchLatinSquare(t *testing.T) {
	entries := []int64{101, 102, 103, 104, 105} // b = 5
	ranks := []ListenerRank{{ID: 1, Rank: 0}, {ID: 2, Rank: 1}, {ID: 3, Rank: 2}, {ID: 4, Rank: 3}, {ID: 5, Rank: 4}}
	plan := PlanBatch(entries, 1, ranks, nil)

	// 同一条（slot）在不同听辨人手里的个人位置互不相同
	for slot, assigns := range plan.Assignments {
		seen := map[int]int{}
		for _, as := range assigns {
			seen[as.OrderIndex]++
		}
		for pos, n := range seen {
			if n > 1 {
				t.Fatalf("slot %d: position %d shared by %d listeners", slot, pos, n)
			}
		}
	}

	// 0..b-1 rank 的 b 个听辨人收听顺序两两不同，每人恰好听到每条一次
	orders := map[string]bool{}
	for _, lr := range ranks {
		slots := make([]int, len(entries))
		count := 0
		for slot, assigns := range plan.Assignments {
			for _, as := range assigns {
				if as.ListenerID == lr.ID {
					if as.OrderIndex < 1 || as.OrderIndex > len(entries) {
						t.Fatalf("order index out of range: %d", as.OrderIndex)
					}
					slots[as.OrderIndex-1] = slot
					count++
				}
			}
		}
		if count != len(entries) {
			t.Fatalf("listener %d got %d trials, want %d", lr.ID, count, len(entries))
		}
		key := ""
		entriesSeen := map[int64]bool{}
		for _, s := range slots {
			key += string(rune('0'+s)) + ","
			entriesSeen[entries[s]] = true
		}
		if len(entriesSeen) != len(entries) {
			t.Fatalf("listener %d did not hear each entry once", lr.ID)
		}
		if orders[key] {
			t.Fatalf("duplicate listening order across listeners: %s", key)
		}
		orders[key] = true
	}
}

func TestPlanBatchPositionsAreGlobalAndContinuous(t *testing.T) {
	entries := []int64{1, 2, 3}
	ranks := []ListenerRank{{ID: 7, Rank: 0}}
	plan := PlanBatch(entries, 11, ranks, nil) // startPosition=11
	for s, tr := range plan.Trials {
		if tr.Position != 11+s {
			t.Fatalf("trial %d position = %d, want %d", s, tr.Position, 11+s)
		}
	}
	if plan.Assignments[0][0].OrderIndex != 1 {
		t.Fatal("first listener first slot order should be 1")
	}
}

func TestPlanBatchContinuesBaseOrder(t *testing.T) {
	// 断点续发：该听辨人此前已收到 4 条，新批次 order_index 从 5 起
	entries := []int64{90, 91}
	ranks := []ListenerRank{{ID: 3, Rank: 0}}
	plan := PlanBatch(entries, 5, ranks, map[int64]int{3: 4})
	for s, assigns := range plan.Assignments {
		for _, as := range assigns {
			if as.OrderIndex != 4+s+1 {
				t.Fatalf("slot %d order = %d, want %d", s, as.OrderIndex, 4+s+1)
			}
		}
	}
}
