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
	mu       sync.Mutex
	elements []entry
	lastpos  int
}

func NewRecent(sizelimit int) Recent {
	return &recent{
		elements: make([]entry, 0, sizelimit),
		lastpos:  0,
	}
}

func (p *recent) Add(timestamp time.Time, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.elements) > 0 {
		last := &p.elements[len(p.elements)-1]
		if last.message == message {
			last.tstamp = timestamp
			last.count++
			return
		}
	}
	if len(p.elements) < cap(p.elements) {
		p.elements = append(p.elements, entry{timestamp, message, 1})
		p.lastpos = (p.lastpos + 1) % cap(p.elements)
		return
	}
	p.elements[p.lastpos] = entry{timestamp, message, 1}
	p.lastpos = (p.lastpos + 1) % len(p.elements)
}

func (p *recent) Format(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := p.lastpos
	for i := 0; i < len(p.elements); i++ {
		if pos--; pos < 0 {
			pos = len(p.elements) - 1
		}
		entry := p.elements[pos]
		var s string
		if entry.count == 1 {
			s = fmt.Sprintf("%s %s\n", entry.tstamp.Format(time.RFC3339), entry.message)
		} else {
			s = fmt.Sprintf("%s %s (x%d)\n", entry.tstamp.Format(time.RFC3339), entry.message, entry.count)
		}
		w.Write([]byte(s))
	}
}
