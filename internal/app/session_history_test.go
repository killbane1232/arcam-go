package app

import (
	"reflect"
	"testing"
	"time"

	"arcam/internal/core"
)

func TestSessionHistoryUsesFrozenSnapshotAndRecordedBalance(t *testing.T) {
	a := testApp(t)
	m := &Sessions{App: a} // No feeds or brokers: history must be entirely read-only.
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bar := func(hour int) core.Candle {
		return core.Candle{Time: at.Add(time.Duration(hour) * time.Hour), Open: 100, High: 112, Low: 99, Close: 110, Volume: 1}
	}
	entry := core.Fill{Time: at.Add(2*time.Hour + time.Second), Action: "enter_long", Price: 100, Quantity: 2}
	exit := core.Fill{Time: at.Add(3*time.Hour + time.Second), Action: "exit_long", Price: 110, Quantity: 2}
	trade := core.Trade{Entry: 100, Exit: 110, Quantity: 2, Fees: 4.2, PnL: 15.8, EntryTime: entry.Time, ExitTime: exit.Time, Side: "long"}
	s := Session{ID: "history", Status: "paused", Request: SessionRequest{Interval: "1h", Symbols: []string{"BTCUSDT", "ETHUSDT"}, Capital: 2000, FeeBPS: 100}, Balances: map[string]float64{"BTCUSDT": 1515.8, "ETHUSDT": 1000}, InputData: map[string][]core.Candle{"BTCUSDT": {bar(0), bar(1), bar(2), bar(3)}}, Previous: map[string]map[string]float64{"BTCUSDT": {"close": 110}}, Events: []SessionEvent{
		{Symbol: "BTCUSDT", Time: at.Add(2 * time.Hour), Decision: &core.Decision{Time: at.Add(2 * time.Hour), Action: "enter_long"}},
		{Symbol: "BTCUSDT", Time: entry.Time, Fill: &entry},
		{Symbol: "BTCUSDT", Time: exit.Time, Fill: &exit, Trade: &trade},
		// A pause leaves a candle with no decision. Do not fabricate a hold for it.
		{Symbol: "BTCUSDT", Time: at.Add(4 * time.Hour), Decision: &core.Decision{Time: at.Add(4 * time.Hour), Action: "hold"}},
	}}
	if e := a.Store.Put("sessions", s.ID, s); e != nil {
		t.Fatal(e)
	}
	h, e := m.History(s.ID)
	if e != nil {
		t.Fatal(e)
	}
	if h.Session.InputData != nil || h.Session.Events != nil || h.Session.Previous != nil {
		t.Fatal("large internal arrays leaked into summary")
	}
	if len(h.Pairs) != 2 {
		t.Fatal(h.Pairs)
	}
	p := h.Pairs[0]
	if len(p.Candles) != 3 || !p.Candles[0].Time.Equal(bar(1).Time) || len(p.Decisions) != 2 || len(p.Events) != 2 || len(p.Trades) != 1 {
		t.Fatalf("wrong projection: %+v", p)
	}
	if len(p.Balance) != 3 || p.Balance[0].Value != 1500 || p.Balance[1].Value != 1498 || p.Balance[2].Value != 1515.8 {
		t.Fatalf("trimmed history must retain its balance anchor: %+v", p.Balance)
	}
	if h.Pairs[1].Candles == nil || h.Pairs[1].Decisions == nil || len(h.Pairs[1].Balance) != 0 {
		t.Fatal("empty pair must have empty arrays")
	}
	var after Session
	if e = a.Store.Get("sessions", s.ID, &after); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(after.InputData, s.InputData) || !reflect.DeepEqual(after.Events, s.Events) || after.Status != "paused" {
		t.Fatal("history changed the persisted session")
	}
}

func TestSessionHistoryLegacyExitDoesNotInventEarlierBalance(t *testing.T) {
	a := testApp(t)
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Session{ID: "legacy-history", Request: SessionRequest{Interval: "1h", Symbols: []string{"BTCUSDT"}, FeeBPS: 10}, Balances: map[string]float64{"BTCUSDT": 999.9}, Events: []SessionEvent{
		{Symbol: "BTCUSDT", Time: at, Fill: &core.Fill{Time: at, Action: "exit_short", Price: 100, Quantity: 1}},
		{Symbol: "BTCUSDT", Time: at.Add(time.Hour), Fill: &core.Fill{Time: at.Add(time.Hour), Action: "enter_long", Price: 100, Quantity: 1}},
	}}
	if e := a.Store.Put("sessions", s.ID, s); e != nil {
		t.Fatal(e)
	}
	h, e := (&Sessions{App: a}).History(s.ID)
	if e != nil {
		t.Fatal(e)
	}
	p := h.Pairs[0]
	if len(p.Candles) != 0 || len(p.Events) != 2 || len(p.Trades) != 0 {
		t.Fatal("legacy events should remain accessible without candles or trades")
	}
	if len(p.Balance) != 2 || p.Balance[0].Value != 1000 || p.Balance[1].Value != 999.9 {
		t.Fatalf("wrong known suffix: %+v", p.Balance)
	}
}
