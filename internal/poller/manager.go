package poller

import (
	"context"
	"sync"
)

// Manager owns the running Poller and swaps it when settings change.
type Manager struct {
	ctx  context.Context
	wg   sync.WaitGroup
	mu   sync.Mutex
	cur  *Poller
	stop chan struct{}
}

// NewManager returns a Manager whose pollers run until ctx is done.
func NewManager(ctx context.Context) *Manager { return &Manager{ctx: ctx} }

// Start runs p, replacing the current poller. The previous poller stops
// scheduling new polls but lets its in-flight polls finish in the background.
func (m *Manager) Start(p *Poller) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stop != nil {
		close(m.stop)
	}
	stop := make(chan struct{})
	m.cur, m.stop = p, stop
	m.wg.Go(func() { p.RunUntil(m.ctx, stop) })
}

// Current returns the running poller, or nil before the first Start.
func (m *Manager) Current() *Poller {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur
}

// Wait blocks until every poller started by m has returned. Cancel the
// Manager's context first.
func (m *Manager) Wait() { m.wg.Wait() }
