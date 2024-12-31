// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package eventlog

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hexian000/gosnippets/slog"
)

type Recent interface {
	Add(timestamp time.Time, message string)
	Format(w io.Writer, n int) error
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
		pos := p.lastpos
		if pos--; pos < 0 {
			pos = len(p.elements) - 1
		}
		last := &p.elements[pos]
		if last.message == message {
			last.tstamp = timestamp
			last.count++
			return
		}
	}
	element := entry{timestamp, message, 1}
	if len(p.elements) < cap(p.elements) {
		p.elements = append(p.elements, element)
		p.lastpos = (p.lastpos + 1) % cap(p.elements)
		return
	}
	p.elements[p.lastpos] = element
	p.lastpos = (p.lastpos + 1) % len(p.elements)
}

func (p *recent) Format(w io.Writer, n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := p.lastpos
	if n > len(p.elements) {
		n = len(p.elements)
	}
	for i := 0; i < n; i++ {
		if pos--; pos < 0 {
			pos = len(p.elements) - 1
		}
		entry := p.elements[pos]
		var s string
		if entry.count == 1 {
			s = fmt.Sprintf("%s %s\n", entry.tstamp.Format(slog.TimeLayout), entry.message)
		} else {
			s = fmt.Sprintf("%s %s (x%d)\n", entry.tstamp.Format(slog.TimeLayout), entry.message, entry.count)
		}
		_, err := w.Write([]byte(s))
		if err != nil {
			return err
		}
	}
	return nil
}
