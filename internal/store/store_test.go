package store

import (
	"sync"
	"testing"

	"arcam/internal/core"
)

func TestVersionsPersistenceAndLock(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := Open(dir); e == nil {
		other.Close()
		t.Fatal("second writer allowed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.SaveVersion("test", core.DemoStrategy()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	v, e := s.Version("test", 1)
	if e != nil {
		t.Fatal(e)
	}
	v.Strategy.Name = "mutated"
	actual, _ := s.Version("test", 1)
	if actual.Strategy.Name == "mutated" {
		t.Fatal("immutable version changed")
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	versions, e := s.Strategies()
	if e != nil || len(versions) != 10 {
		t.Fatalf("persistence %d %v", len(versions), e)
	}
	if _, e = s.SaveVersion("../escape", core.DemoStrategy()); e == nil {
		t.Fatal("unsafe ID allowed")
	}
}
func TestCandleMerge(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	bars := core.DemoCandles(100)
	d := Dataset{Source: "csv", Symbol: "BTCUSDT", Interval: "1h", Candles: bars[:10]}
	if _, e = s.Merge(d); e != nil {
		t.Fatal(e)
	}
	d.Candles = bars[5:15]
	info, e := s.Merge(d)
	if e != nil || info.Count != 15 {
		t.Fatalf("merge %v %v", info, e)
	}
}
