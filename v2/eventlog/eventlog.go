package eventlog

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type Recent interface {
	Add(timestamp time.Time, message string)
	Format(w io.Writer)
}

type entry struct {
	tstamp  time.Time
	message string
	count   int
}

type recent struct {
	mu        sync.Mutex
	elements  []entry
	sizelimit int
}

func NewRecent(sizelimit int) Recent {
	return &recent{
		elements:  []entry{},
		sizelimit: sizelimit,
	}
}

func (p *recent) Add(timestamp time.Time, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.elements) > 0 && p.elements[len(p.elements)-1].message == message {
		p.elements[len(p.elements)-1].tstamp = timestamp
		p.elements[len(p.elements)-1].count++
		return
	}
	p.elements = append(p.elements, entry{timestamp, message, 1})
	if len(p.elements) >= 2*p.sizelimit {
		copy(p.elements, p.elements[len(p.elements)-p.sizelimit:])
		p.elements = p.elements[:p.sizelimit]
	}
}

func (p *recent) Format(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := max(0, len(p.elements)-p.sizelimit)
	for i := len(p.elements) - 1; i >= n; i-- {
		entry := p.elements[i]
		var s string
		if entry.count == 1 {
			s = fmt.Sprintf("%s %s\n", entry.tstamp.Format(time.RFC3339), entry.message)
		} else {
			s = fmt.Sprintf("%s %s (x%d)\n", entry.tstamp.Format(time.RFC3339), entry.message, entry.count)
		}
		w.Write([]byte(s))
	}
}
