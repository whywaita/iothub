package iotdevice

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/goautomotive/iothub/common"
)

// once is like sync.Once but if fn returns an error it's considered
// as a failure and it's still can be called until it returns a nil error.
func once(i *uint32, mu *sync.RWMutex, fn func() error) error {
	// make a quick check without locking the mutex
	if atomic.LoadUint32(i) == 1 {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	// someone can run the given func and change the value
	// between atomic checking and lock acquiring
	if *i == 1 {
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	atomic.StoreUint32(i, 1)
	return nil
}

type eventsMux struct {
	on   uint32
	mu   sync.RWMutex
	subs []*EventSub
	done chan struct{}
}

func (m *eventsMux) once(fn func() error) error {
	return once(&m.on, &m.mu, fn)
}

func (m *eventsMux) Dispatch(msg *common.Message) {
	m.mu.RLock()
	for _, sub := range m.subs {
		select {
		case sub.ch <- msg:
		default:
			go func() {
				select {
				case sub.ch <- msg:
				case <-m.done:
				}
			}()
		}
	}
	m.mu.RUnlock()
}

func (m *eventsMux) sub() *EventSub {
	s := &EventSub{ch: make(chan *common.Message, 10)}
	m.mu.Lock()
	m.subs = append(m.subs, s)
	m.mu.Unlock()
	return s
}

func (m *eventsMux) unsub(s *EventSub) {
	m.mu.Lock()
	for i, ss := range m.subs {
		if ss == s {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *eventsMux) close(err error) {
	m.mu.Lock()
	for _, s := range m.subs {
		s.err = ErrClosed
		close(s.ch)
	}
	m.subs = m.subs[0:0]
	m.mu.Unlock()
}

type EventSub struct {
	ch  chan *common.Message
	err error
}

func (s *EventSub) C() <-chan *common.Message {
	return s.ch
}

func (s *EventSub) Err() error {
	return s.err
}

type twinStateMux struct {
	on   uint32
	mu   sync.RWMutex
	subs []*TwinStateSub
	done chan struct{}
}

func (m *twinStateMux) once(fn func() error) error {
	return once(&m.on, &m.mu, fn)
}

func (m *twinStateMux) Dispatch(b []byte) {
	var v TwinState
	if err := json.Unmarshal(b, &v); err != nil {
		log.Printf("unmarshal error: %s", err) // TODO
		return
	}

	m.mu.RLock()
	for _, sub := range m.subs {
		select {
		case sub.ch <- v:
		default:
			go func() {
				select {
				case sub.ch <- v:
				case <-m.done:
				}
			}()
		}
	}
	m.mu.RUnlock()
}

func (m *twinStateMux) sub() *TwinStateSub {
	s := &TwinStateSub{ch: make(chan TwinState, 10)}
	m.mu.Lock()
	m.subs = append(m.subs, s)
	m.mu.Unlock()
	return s
}

func (m *twinStateMux) unsub(s *TwinStateSub) {
	m.mu.Lock()
	for i, ss := range m.subs {
		if ss == s {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *twinStateMux) close(err error) {
	m.mu.Lock()
	for _, s := range m.subs {
		s.err = ErrClosed
		close(s.ch)
	}
	m.subs = m.subs[0:0]
	m.mu.Unlock()
}

type TwinStateSub struct {
	ch  chan TwinState
	err error
}

func (s *TwinStateSub) C() <-chan TwinState {
	return s.ch
}

func (s *TwinStateSub) Err() error {
	return s.err
}

// methodMux is direct-methods dispatcher.
type methodMux struct {
	on uint32
	mu sync.RWMutex
	m  map[string]DirectMethodHandler
}

func (m *methodMux) once(fn func() error) error {
	return once(&m.on, &m.mu, fn)
}

// handle registers the given direct-method handler.
func (m *methodMux) handle(method string, fn DirectMethodHandler) error {
	if fn == nil {
		panic("fn is nil")
	}
	m.mu.Lock()
	if m.m == nil {
		m.m = map[string]DirectMethodHandler{}
	}
	if _, ok := m.m[method]; ok {
		m.mu.Unlock()
		return fmt.Errorf("method %q is already registered", method)
	}
	m.m[method] = fn
	m.mu.Unlock()
	return nil
}

// remove deregisters the named method.
func (m *methodMux) remove(method string) {
	m.mu.Lock()
	if m.m != nil {
		delete(m.m, method)
	}
	m.mu.Unlock()
}

// Dispatch dispatches the named method, error is not nil only when dispatching fails.
func (m *methodMux) Dispatch(method string, b []byte) (int, []byte, error) {
	m.mu.RLock()
	f, ok := m.m[method]
	m.mu.RUnlock()
	if !ok {
		return 0, nil, fmt.Errorf("method %q is not registered", method)
	}

	var v map[string]interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return jsonErr(err)
	}
	v, err := f(v)
	if err != nil {
		return jsonErr(err)
	}
	if v == nil {
		v = map[string]interface{}{}
	}
	b, err = json.Marshal(v)
	if err != nil {
		return jsonErr(err)
	}
	return 200, b, nil
}

func jsonErr(err error) (int, []byte, error) {
	return 500, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())), nil
}
