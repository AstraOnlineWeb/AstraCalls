package main

import (
	"testing"
	"time"
)

// o timer de toque deve disparar quando a chamada continua no registro (tocando).
func TestRingTimerFiresWhenStillRinging(t *testing.T) {
	r := newCallRegistry()
	r.add("c1", &activeCall{})
	fired := make(chan struct{}, 1)
	t1 := time.AfterFunc(10*time.Millisecond, func() { fired <- struct{}{} })
	r.setRingTimer("c1", t1)
	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timer de toque não disparou para chamada ainda tocando")
	}
}

// remover a chamada (encerramento normal) deve cancelar o timer.
func TestRemoveStopsRingTimer(t *testing.T) {
	r := newCallRegistry()
	r.add("c1", &activeCall{})
	fired := make(chan struct{}, 1)
	t1 := time.AfterFunc(30*time.Millisecond, func() { fired <- struct{}{} })
	r.setRingTimer("c1", t1)
	r.remove("c1")
	select {
	case <-fired:
		t.Fatal("timer disparou mesmo após remove() (deveria ter sido cancelado)")
	case <-time.After(80 * time.Millisecond):
	}
}

// atender (stopRingTimer) deve cancelar o timer.
func TestStopRingTimerCancels(t *testing.T) {
	r := newCallRegistry()
	r.add("c1", &activeCall{})
	fired := make(chan struct{}, 1)
	t1 := time.AfterFunc(30*time.Millisecond, func() { fired <- struct{}{} })
	r.setRingTimer("c1", t1)
	r.stopRingTimer("c1")
	select {
	case <-fired:
		t.Fatal("timer disparou mesmo após stopRingTimer() (chamada atendida)")
	case <-time.After(80 * time.Millisecond):
	}
}

// setRingTimer numa chamada que já saiu do registro deve parar o timer na hora.
func TestSetRingTimerOnMissingCallStopsIt(t *testing.T) {
	r := newCallRegistry()
	fired := make(chan struct{}, 1)
	t1 := time.AfterFunc(10*time.Millisecond, func() { fired <- struct{}{} })
	r.setRingTimer("inexistente", t1) // sem add: deve parar o timer
	select {
	case <-fired:
		t.Fatal("timer disparou para chamada inexistente (deveria ter sido parado)")
	case <-time.After(80 * time.Millisecond):
	}
}
