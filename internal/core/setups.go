package core

import (
	"fmt"
	"time"
)

// SetupState is immutable once published in a decision/checkpoint. LastKey is
// a high-water mark: explicit event keys must be increasing (e.g. level time).
type SetupState struct {
	Reason        string             `json:"reason,omitempty"`
	Status        string             `json:"status"`
	WasTrue       bool               `json:"was_true"`
	HasKey        bool               `json:"has_key,omitempty"`
	LastKey       float64            `json:"last_key,omitempty"`
	EventID       string             `json:"event_id,omitempty"`
	ReferenceTime time.Time          `json:"reference_time,omitempty"`
	ExpiresAt     time.Time          `json:"expires_at,omitempty"`
	Captured      map[string]float64 `json:"captured,omitempty"`
}

func injectSetup(f map[string]float64, id string, state SetupState, at time.Time, step time.Duration) {
	prefix := "setup." + id + "."
	f[prefix+"active"] = 0
	if state.Status == "waiting" && state.Reason == "" {
		f[prefix+"active"] = 1
	}
	if state.Reason != "" && (state.Status == "waiting" || state.Status == "unavailable") {
		delete(f, prefix+"active")
	}
	if state.ReferenceTime.IsZero() {
		return
	}
	f[prefix+"age"] = float64(at.Sub(state.ReferenceTime) / step)
	f[prefix+"reference_time"] = float64(state.ReferenceTime.Unix())
	f[prefix+"expires_at"] = float64(state.ExpiresAt.Unix())
	for k, v := range state.Captured {
		f[prefix+k] = v
	}
}

func advanceSetups(s Strategy, old map[string]SetupState, now, prev map[string]float64, at time.Time, step time.Duration, side string) map[string]SetupState {
	if len(s.Setups) == 0 {
		return nil
	}
	states := make(map[string]SetupState, len(s.Setups))
	for _, setup := range s.Setups {
		state := old[setup.ID]
		state.Reason = ""
		injectSetup(now, setup.ID, state, at, step)
		ctx := ruleContext{position: "none"}
		if side != "" {
			ctx.position = side
		}
		if state.Status == "waiting" {
			if at.After(state.ExpiresAt) {
				state.Status = "expired"
			} else if setup.Cancel != nil {
				t := evaluate(setup.Cancel, now, prev, ctx)
				if !t.Ready {
					state.Reason = "cancellation values unavailable"
				} else if t.Result {
					state.Status = "cancelled"
				}
			}
		}
		trigger := evaluate(setup.When, now, prev, ctx)
		matched := trigger.Ready && trigger.Result
		key, keyReady := float64(at.Unix()), true
		fresh := matched && !state.WasTrue
		if setup.Key != nil {
			key, keyReady = operand(setup.Key, now)
			fresh = matched && keyReady && (!state.HasKey || key > state.LastKey)
		}
		// Missing trigger data does not rearm a still-true event.
		if trigger.Ready {
			state.WasTrue = matched
		}
		if side == "" && state.Status != "waiting" && fresh && keyReady {
			captured := map[string]float64{}
			ready := true
			for name, o := range setup.Capture {
				v, ok := operand(&o, now)
				if !ok {
					state.Status = "unavailable"
					state.Reason = "capture values unavailable"
					ready = false
					break
				}
				captured[name] = v
			}
			if ready {
				state = SetupState{Status: "waiting", WasTrue: matched, HasKey: true, LastKey: key, EventID: fmt.Sprintf("%s:%g", setup.ID, key), ReferenceTime: at, ExpiresAt: at.Add(time.Duration(setup.ExpiresAfter) * step), Captured: captured}
				injectSetup(now, setup.ID, state, at, step)
				if setup.Cancel != nil {
					t := evaluate(setup.Cancel, now, prev, ctx)
					if !t.Ready {
						state.Reason = "cancellation values unavailable"
					} else if t.Result {
						state.Status = "cancelled"
					}
				}
			}
		}
		states[setup.ID] = state
		injectSetup(now, setup.ID, state, at, step)
	}
	return states
}

func matchedSetups(t Trace, ids map[string]bool) {
	if !t.Ready || !t.Result || t.Skipped {
		return
	}
	if t.Setup != "" {
		ids[t.Setup] = true
	}
	for _, child := range t.Children {
		matchedSetups(child, ids)
	}
}
func consumeSetups(states map[string]SetupState, t Trace) string {
	ids := map[string]bool{}
	matchedSetups(t, ids)
	// Iterate trace order for stable audit IDs rather than map order.
	event := ""
	var walk func(Trace)
	walk = func(v Trace) {
		if v.Setup != "" && ids[v.Setup] {
			state := states[v.Setup]
			if state.Status == "waiting" {
				state.Status = "consumed"
				states[v.Setup] = state
				if event != "" {
					event += ","
				}
				event += state.EventID
			}
			delete(ids, v.Setup)
		}
		for _, c := range v.Children {
			walk(c)
		}
	}
	walk(t)
	return event
}
