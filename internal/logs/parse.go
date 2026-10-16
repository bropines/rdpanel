package logs

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// parser turns raw lines of one source into entries. It remembers the last
// header's time and level: lines without one (hbbs' multi-line SQL logs, Go
// panics, stack traces) are continuations and inherit them.
type parser struct {
	source    string
	lastTime  time.Time
	lastLevel Level
	seen      bool
}

func (p *parser) parse(line string, now time.Time) Entry {
	if e, ok := ParseHBB(line); ok {
		p.remember(e)
		return e
	}
	if e, ok := ParseSlogJSON(line); ok {
		p.remember(e)
		return e
	}
	if e, ok := ParseSlogText(line); ok {
		p.remember(e)
		return e
	}
	e := Entry{Message: strings.TrimSpace(line), Level: LevelInfo, Time: now}
	if p.seen {
		e.Level, e.Time = p.lastLevel, p.lastTime
	}
	if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") {
		e.Level = LevelError
	}
	return e
}

func (p *parser) remember(e Entry) {
	p.lastTime, p.lastLevel, p.seen = e.Time, e.Level, true
}

// hbbs/hbbr (flexi_logger default format):
//
//	[2026-10-16 13:27:07.704684 +03:00] INFO [src/rendezvous_server.rs:1671] Key: ...
var hbbHeader = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(?:\.\d+)? [+-]\d\d:?\d\d)\] +([A-Za-z]+) +(?:\[([^\]]*)\] ?)?(.*)$`)

const hbbTimeLayout = "2006-01-02 15:04:05.999999999 -07:00"

// ParseHBB parses one hbbs/hbbr log line. The message is the text after the
// source location (which stays in Raw).
func ParseHBB(line string) (Entry, bool) {
	m := hbbHeader.FindStringSubmatch(line)
	if m == nil {
		return Entry{}, false
	}
	lvl, ok := ParseLevel(m[2])
	if !ok {
		return Entry{}, false
	}
	ts := m[1]
	if !strings.Contains(ts[len(ts)-6:], ":") { // +0300 -> +03:00
		ts = ts[:len(ts)-2] + ":" + ts[len(ts)-2:]
	}
	t, err := time.Parse(hbbTimeLayout, ts)
	if err != nil {
		return Entry{}, false
	}
	return Entry{Time: t.UTC(), Level: lvl, Message: m[4]}, true
}

// ParseSlogJSON parses a log/slog JSONHandler line: time, level and msg
// become the entry's fields, the remaining attributes are appended to the
// message as key=value in their original order.
func ParseSlogJSON(line string) (Entry, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return Entry{}, false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return Entry{}, false
	}
	var (
		e        Entry
		msg      string
		attrs    []string
		hasLevel bool
		hasMsg   bool
	)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return Entry{}, false
		}
		k, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Entry{}, false
		}
		var str string
		isStr := json.Unmarshal(raw, &str) == nil
		switch {
		case k == "time" && isStr && e.Time.IsZero():
			if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
				e.Time = t.UTC()
				continue
			}
		case k == "level" && isStr && !hasLevel:
			e.Level = slogLevel(str)
			hasLevel = true
			continue
		case k == "msg" && isStr && !hasMsg:
			msg, hasMsg = str, true
			continue
		}
		v := str
		if !isStr {
			var cb bytes.Buffer
			if json.Compact(&cb, raw) == nil {
				v = cb.String()
			} else {
				v = string(raw)
			}
		} else if v == "" || strings.ContainsAny(v, " \t\"=") {
			v = quote(v)
		}
		attrs = append(attrs, k+"="+v)
	}
	if !hasLevel || !hasMsg {
		return Entry{}, false
	}
	e.Message = joinMsg(msg, attrs)
	return e, true
}

// slogLevel maps "INFO", "WARN+2", "DEBUG-4" to a Level.
func slogLevel(s string) Level {
	base := s
	if i := strings.IndexAny(s, "+-"); i > 0 {
		base = s[:i]
	}
	l, _ := ParseLevel(base)
	return l
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func joinMsg(msg string, attrs []string) string {
	if len(attrs) == 0 {
		return msg
	}
	if msg == "" {
		return strings.Join(attrs, " ")
	}
	return msg + " " + strings.Join(attrs, " ")
}

// ParseSlogText parses a log/slog TextHandler (logfmt) line such as
//
//	time=2026-10-16T13:27:07.704+03:00 level=INFO msg="starting rdpanel" version=0.2.0
func ParseSlogText(line string) (Entry, bool) {
	if !strings.HasPrefix(line, "time=") && !strings.HasPrefix(line, "level=") {
		return Entry{}, false
	}
	var (
		e                Entry
		msg              string
		attrs            []string
		hasLevel, hasMsg bool
	)
	rest := line
	for rest != "" {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			break
		}
		eq := strings.IndexByte(rest, '=')
		sp := strings.IndexByte(rest, ' ')
		if eq <= 0 || (sp >= 0 && sp < eq) {
			return Entry{}, false
		}
		k := rest[:eq]
		rest = rest[eq+1:]
		var v, rawV string
		if strings.HasPrefix(rest, `"`) {
			end := 1
			for end < len(rest) && rest[end] != '"' {
				if rest[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(rest) {
				return Entry{}, false
			}
			rawV = rest[:end+1]
			if err := json.Unmarshal([]byte(rawV), &v); err != nil {
				v = rawV[1 : len(rawV)-1]
			}
			rest = rest[end+1:]
		} else {
			if sp = strings.IndexByte(rest, ' '); sp < 0 {
				sp = len(rest)
			}
			v, rawV = rest[:sp], rest[:sp]
			rest = rest[sp:]
		}
		switch {
		case k == "time" && e.Time.IsZero():
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				e.Time = t.UTC()
				continue
			}
		case k == "level" && !hasLevel:
			e.Level, hasLevel = slogLevel(v), true
			continue
		case k == "msg" && !hasMsg:
			msg, hasMsg = v, true
			continue
		}
		attrs = append(attrs, k+"="+rawV)
	}
	if !hasLevel || !hasMsg {
		return Entry{}, false
	}
	e.Message = joinMsg(msg, attrs)
	return e, true
}
