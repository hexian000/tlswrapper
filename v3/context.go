// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"context"
	"sync"
	"time"
)

type contextMgr struct {
	timeout  func() time.Duration
	mu       sync.Mutex
	contexts map[context.Context]context.CancelFunc
}

func (m *contextMgr) withTimeout() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.contexts == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout())
	m.contexts[ctx] = cancel
	return ctx
}

func (m *contextMgr) cancel(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.contexts == nil {
		return
	}
	if cancel, ok := m.contexts[ctx]; ok {
		cancel()
		delete(m.contexts, ctx)
	}
}

func (m *contextMgr) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cancel := range m.contexts {
		cancel()
	}
	m.contexts = nil
}
