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
	g := &group{
		closeCh: make(chan struct{}),
	}
	if limit > 0 {
		g.routineCh = make(chan struct{}, limit)
	}
	return g
}

func (g *group) wrapper(f func()) {
	defer func() {
		if g.routineCh != nil {
			<-g.routineCh
		}
		g.wg.Done()
	}()
	f()
}

func (g *group) Go(f func()) error {
	if g.routineCh != nil {
		select {
		case <-g.closeCh:
			return ErrStopping
		case g.routineCh <- struct{}{}:
		default:
			return ErrConcurrencyLimit
		}
	} else {
		select {
		case <-g.closeCh:
			return ErrStopping
		default:
		}
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

func (g *group) Wait() {
	g.wg.Wait()
}
