package app

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunUpdatesExposeCommittedPrefixesAndDeltas(t *testing.T) {
	s := testApp(t)
	waiting, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	r := progressRequest(t, s, func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 3 {
			close(waiting)
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		writeProgressVerdict(w)
	})
	t.Cleanup(unblock)
	r.FeeBPS = 10
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("model not reached")
	}
	u, e := s.RunUpdates(run.ID, nil)
	if e != nil || u.Run.Status != "running" || u.Run.InputData != nil || u.Run.Pairs != nil || len(u.Pairs) != 1 {
		t.Fatal("bad live update", u, e)
	}
	p := u.Pairs[0].Pair
	if len(p.Candles) != 2 || len(p.Decisions) != 2 || len(p.Equity) != 2 || p.OpenPosition == nil || p.Complete || p.Metrics.Initial != r.Capital/2 || p.Metrics.Fees <= 0 {
		t.Fatal("inconsistent live prefix or incorrect per-pair capital", p)
	}
	after := map[string]PairCursor{p.Symbol: u.Pairs[0].Next}
	// Returned updates own their maps, slices and position pointers.
	p.Candles[0].Close = -1
	p.Decisions[0].Features["close"] = -1
	p.OpenPosition.Entry = -1
	copy, e := s.Run(run.ID)
	if e != nil || copy.Pairs[0].Candles[0].Close <= 0 || copy.Pairs[0].Decisions[0].Features["close"] <= 0 || copy.Pairs[0].OpenPosition.Entry <= 0 {
		t.Fatal("client mutated live state", e)
	}
	empty, e := s.RunUpdates(run.ID, after)
	if e != nil || len(empty.Pairs[0].Pair.Candles) != 0 || len(empty.Pairs[0].Pair.Fills) != 0 || empty.Pairs[0].Pair.OpenPosition == nil {
		t.Fatal("unchanged poll resent history or lost position", empty, e)
	}
	unblock()
	finished := waitRun(t, s, run.ID)
	delta, e := s.RunUpdates(run.ID, after)
	if e != nil || delta.Run.Status != "completed" || len(delta.Pairs) != 2 || len(delta.Pairs[0].Pair.Candles) != 1 || len(delta.Pairs[1].Pair.Candles) != 3 {
		t.Fatal("missing or duplicated delta across pair transition", delta, e)
	}
	for i, d := range delta.Pairs {
		if !d.Pair.Complete || d.Pair.OpenPosition != nil || d.Next.Candles != len(finished.Pairs[i].Candles) || len(d.Pair.Candles) != len(d.Pair.Decisions) || len(d.Pair.Candles) != len(d.Pair.Equity) {
			t.Fatal("inconsistent completed delta", d)
		}
		after[d.Pair.Symbol] = d.Next
	}
	empty, e = s.RunUpdates(run.ID, after)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range empty.Pairs {
		if len(d.Pair.Candles)+len(d.Pair.Decisions)+len(d.Pair.Equity)+len(d.Pair.Fills)+len(d.Pair.Trades) != 0 {
			t.Fatal("duplicate rows after catching up", d)
		}
	}
	reset, e := s.RunUpdates(run.ID, map[string]PairCursor{"BTCUSDT": {Candles: 999}})
	if e != nil || !reset.Pairs[0].Reset || reset.Pairs[0].From.Candles != 0 || len(reset.Pairs[0].Pair.Candles) != 3 {
		t.Fatal("stale client could not recover", reset, e)
	}
	if _, e := s.RunUpdates(run.ID, map[string]PairCursor{"BTCUSDT": {Candles: -1}}); e == nil {
		t.Fatal("negative cursor accepted")
	}
}
