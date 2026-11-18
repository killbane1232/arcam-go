package app

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/execution"
	"arcam/internal/store"
)

func TestRestartRecoversStatusesWithoutChangingSavedState(t *testing.T) {
	t.Setenv("ENABLE_LIVE_TRADING", "false")
	t.Setenv("MAX_LIVE_ORDER_NOTIONAL", "100")
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	runs := map[string]core.Run{}
	for _, status := range []string{"running", "queued", "preparing", "failed", "completed", "cancelled", "interrupted"} {
		r := core.Run{ID: status, Status: status, Error: "saved error", InputData: map[string][]core.Candle{"BTCUSDT": {{Time: at, Close: 100}}}, Pairs: []core.PairResult{{Symbol: "BTCUSDT", PendingPlan: &core.EntryPlan{Side: "long", StopPrice: 90}, Decisions: []core.Decision{{Features: map[string]float64{"close": 100}}}}}}
		runs[status] = r
		if err = st.Put("runs", status, r); err != nil {
			t.Fatal(err)
		}
	}
	sessions := map[string]Session{}
	for _, status := range []string{"running", "paused", "stopped"} {
		s := Session{ID: status, Status: status, Error: "saved error", Request: SessionRequest{Mode: "live"}, Positions: map[string]core.Position{"BTCUSDT": {Side: "long", Quantity: 1, StopPrice: 90, TargetPrice: 120, InitialRisk: 10}},
			Pending: &Intent{Action: "enter_long", Order: execution.Order{Symbol: "BTCUSDT", ClientID: "keep-me", Quantity: "1"}, Plan: &core.EntryPlan{Side: "long", StopPrice: 90, SignalTime: at}, BalanceBefore: 1000},
			Setups:  map[string]map[string]core.SetupState{"BTCUSDT": {"level": {Status: "consumed", ReferenceTime: at, Captured: map[string]float64{"stop": 90}}}}, Daily: map[string]DailyEntries{"BTCUSDT": {Day: "2026-11-18", Count: 1}}, InputData: map[string][]core.Candle{"BTCUSDT": {{Time: at, Close: 100}}}}
		sessions[status] = s
		if err = st.Put("sessions", status, s); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	m, err := NewSessions(svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.jobs) != 0 || len(m.workers) != 0 {
		t.Fatal("restart launched execution automatically")
	}
	for id, original := range runs {
		var got core.Run
		if err = st.Get("runs", id, &got); err != nil {
			t.Fatal(err)
		}
		if id == "running" || id == "queued" || id == "preparing" {
			original.Status, original.Error = "interrupted", "service restarted; submit a new run"
		}
		assertSavedJSON(t, original, got)
	}
	for id, original := range sessions {
		var got Session
		if err = st.Get("sessions", id, &got); err != nil {
			t.Fatal(err)
		}
		if id == "running" {
			original.Status, original.Error = "paused", "service restarted; reconcile and resume explicitly"
		}
		assertSavedJSON(t, original, got)
	}
}

func assertSavedJSON(t *testing.T, want, got any) {
	t.Helper()
	a, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("restart changed saved state:\nwant %s\ngot %s", a, b)
	}
}
