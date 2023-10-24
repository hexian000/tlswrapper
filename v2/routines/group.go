package routines

import (
	"errors"
	"sync"
)

var (
	ErrStopping         = errors.New("service is stopping")
	ErrConcurrencyLimit = errors.New("concurrency limit is exceeded")
)

type Group interface {
	Go(func()) error
	Count() int
	Close()
	CloseC() <-chan struct{}
	Wait()
}

type group struct {
	wg        sync.WaitGroup
	routineCh chan struct{}
	closeCh   chan struct{}
}

func NewGroup(limit uint32) Group {
	return &group{
		routineCh: make(chan struct{}, limit),
		closeCh:   make(chan struct{}),
	}
}

func (g *group) wrapper(f func()) {
	defer func() {
		<-g.routineCh
		g.wg.Done()
	}()
	f()
}

func (g *group) Go(f func()) error {
	select {
	case <-g.closeCh:
		return ErrStopping
	case g.routineCh <- struct{}{}:
	default:
		return ErrConcurrencyLimit
	}
	g.wg.Add(1)
	go g.wrapper(f)
	return nil
}

func (g *group) Close() {
	close(g.closeCh)
}

func (g *group) CloseC() <-chan struct{} {
	return g.closeCh
}

func (g *group) Count() int {
	return len(g.routineCh)
}

func (g *group) Wait() {
	g.wg.Wait()
}
