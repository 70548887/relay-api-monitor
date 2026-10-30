package update

import "sync"

// TryReserveOperation coordinates HTTP acceptance with automatic checks before
// either reads state or starts asynchronous work. Check/Download/Apply keep
// their internal state locks; callers retain this reservation across a chain
// such as automatic checking followed by downloading.
func (m *Manager) TryReserveOperation() (release func(), ok bool) {
	if !m.operationMu.TryLock() {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(m.operationMu.Unlock) }, true
}
