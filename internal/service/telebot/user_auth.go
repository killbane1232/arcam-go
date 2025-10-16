package telebot

import (
	"time"
)

// UserStateManager методы для управления состояниями
func (usm *UserStateManager) GetState(userID int64) *UserState {
	usm.mu.RLock()
	defer usm.mu.RUnlock()

	state, exists := usm.states[userID]
	if !exists {
		state = &UserState{
			CurrentState: StateMainMenu,
			Data:         make(map[string]interface{}),
			LastActive:   time.Now().Unix(),
		}
		usm.states[userID] = state
	}

	return state
}

func (usm *UserStateManager) SetState(userID int64, state StateType) {
	usm.mu.Lock()
	defer usm.mu.Unlock()

	if _, exists := usm.states[userID]; !exists {
		usm.states[userID] = &UserState{
			Data: make(map[string]interface{}),
		}
	}

	usm.states[userID].CurrentState = state
	usm.states[userID].LastActive = time.Now().Unix()
}

func (usm *UserStateManager) UpdateLastActive(userID int64) {
	usm.mu.Lock()
	defer usm.mu.Unlock()

	if state, exists := usm.states[userID]; exists {
		state.LastActive = time.Now().Unix()
	}
}

func (usm *UserStateManager) SetStateData(userID int64, key string, value interface{}) {
	usm.mu.Lock()
	defer usm.mu.Unlock()

	if _, exists := usm.states[userID]; !exists {
		usm.states[userID] = &UserState{
			CurrentState: StateMainMenu,
			Data:         make(map[string]interface{}),
			LastActive:   time.Now().Unix(),
		}
	}

	usm.states[userID].Data[key] = value
	usm.states[userID].LastActive = time.Now().Unix()
}

func (usm *UserStateManager) GetStateData(userID int64, key string) interface{} {
	usm.mu.RLock()
	defer usm.mu.RUnlock()

	if state, exists := usm.states[userID]; exists {
		return state.Data[key]
	}

	return nil
}

func (usm *UserStateManager) ClearState(userID int64) {
	usm.mu.Lock()
	defer usm.mu.Unlock()

	delete(usm.states, userID)
}

// CleanupInactive очищает неактивные сессии (старше 24 часов)
func (usm *UserStateManager) CleanupInactive() {
	usm.mu.Lock()
	defer usm.mu.Unlock()

	now := time.Now().Unix()
	for userID, state := range usm.states {
		if now-state.LastActive > 24*60*60 { // 24 часа
			delete(usm.states, userID)
		}
	}
}
