package main

import (
	"bytes"
	"time"
)

const (
	bpStart  = "\x1b[200~"
	bpEnd    = "\x1b[201~"
	maxPaste = 2 << 20 // 2MiB：超过则放弃拦截原样放行
)

type EventType int

const (
	EvForward EventType = iota // 逐字节透传
	EvPaste                    // 一段完整括号粘贴的载荷（不含标记）
)

type Event struct {
	Type EventType
	Data []byte
}

// Scanner 是 stdin 方向的状态机流过滤器。
// 除完整括号粘贴外，一切字节原样转发。
type Scanner struct {
	inPaste bool
	pending []byte // 块尾滞留的疑似半截标记
	paste   []byte
}

func (s *Scanner) InPaste() bool { return s.inPaste }

// HasPending: 块尾滞留着疑似半截标记（注入方须避开此刻，防止劈开用户的转义序列）。
func (s *Scanner) HasPending() bool { return len(s.pending) > 0 }

func (s *Scanner) Feed(chunk []byte) []Event {
	var evs []Event
	data := append(s.pending, chunk...)
	s.pending = nil
	for len(data) > 0 {
		if s.inPaste {
			if i := bytes.Index(data, []byte(bpEnd)); i >= 0 {
				s.paste = append(s.paste, data[:i]...)
				evs = append(evs, Event{EvPaste, s.paste})
				s.paste = nil
				s.inPaste = false
				data = data[i+len(bpEnd):]
				continue
			}
			keep := partialSuffix(data, []byte(bpEnd))
			s.paste = append(s.paste, data[:len(data)-keep]...)
			s.pending = append([]byte{}, data[len(data)-keep:]...)
			if len(s.paste) > maxPaste {
				out := append([]byte(bpStart), s.paste...)
				evs = append(evs, Event{EvForward, out})
				s.paste = nil
				s.inPaste = false
			}
			data = nil
			continue
		}
		if i := bytes.Index(data, []byte(bpStart)); i >= 0 {
			if i > 0 {
				evs = append(evs, Event{EvForward, append([]byte{}, data[:i]...)})
			}
			s.inPaste = true
			data = data[i+len(bpStart):]
			continue
		}
		keep := partialSuffix(data, []byte(bpStart))
		if len(data)-keep > 0 {
			evs = append(evs, Event{EvForward, append([]byte{}, data[:len(data)-keep]...)})
		}
		s.pending = append([]byte{}, data[len(data)-keep:]...)
		data = nil
	}
	return evs
}

// Idle 由空闲定时器调用（sinceLast = 距最后一次 Feed 的时长）。
// 非粘贴态：吐出滞留的半截标记（它其实是用户敲的普通字节）。
// 粘贴态 >2s 未闭合：中止拦截，把起始标记+已收内容原样放行。
func (s *Scanner) Idle(sinceLast time.Duration) []Event {
	if s.inPaste {
		if sinceLast < 2*time.Second {
			return nil
		}
		out := append([]byte(bpStart), s.paste...)
		out = append(out, s.pending...)
		s.paste, s.pending, s.inPaste = nil, nil, false
		return []Event{{EvForward, out}}
	}
	if len(s.pending) == 0 || sinceLast < 50*time.Millisecond {
		return nil
	}
	ev := Event{EvForward, s.pending}
	s.pending = nil
	return []Event{ev}
}

// partialSuffix 返回 data 的最长后缀长度，该后缀是 marker 的真前缀。
func partialSuffix(data, marker []byte) int {
	max := len(marker) - 1
	if max > len(data) {
		max = len(data)
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(data[len(data)-n:], marker[:n]) {
			return n
		}
	}
	return 0
}
