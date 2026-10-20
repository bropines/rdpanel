package db

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// ErrMixedPlaceholders is returned for a query that mixes plain ('?') and
// numbered ('?N') placeholders. SQLite numbers a plain '?' after the largest
// index seen so far, which has no faithful PostgreSQL equivalent.
var ErrMixedPlaceholders = errors.New("db: query mixes ? and ?N placeholders")

// Rebind converts SQLite parameter placeholders in query to PostgreSQL ones:
// plain '?' become sequential '$1', '$2', ... and numbered '?N' become '$N'
// (so a value bound once can be referenced several times). A query that mixes
// both styles is rejected with ErrMixedPlaceholders.
//
// Single-quoted literals ('...'), double-quoted identifiers ("..."), line
// comments (-- ...) and block comments (/* ... */) are copied verbatim.
func Rebind(query string) (string, error) {
	q, _, _, err := rewrite(query, false, nil)
	return q, err
}

// postgresQuery adapts a query written for SQLite (the reference dialect of
// the repositories) and its arguments to PostgreSQL:
//
//   - placeholders are rebound as in Rebind;
//   - LIKE becomes ILIKE: SQLite's LIKE is case-insensitive (ASCII),
//     PostgreSQL's is not, and searches must behave the same on both;
//   - arguments no numbered placeholder refers to are dropped (and the
//     placeholders renumbered), as are all arguments of a query without
//     placeholders: SQLite ignores them, PostgreSQL cannot infer their type
//     and rejects the statement;
//   - Go booleans are bound as 0/1 (see postgresArgs).
func postgresQuery(query string, args []any) (string, []any, error) {
	q, used, plain, err := rewrite(query, true, nil)
	if err != nil {
		return "", nil, err
	}
	if len(used) == 0 && plain == 0 {
		return q, nil, nil
	}
	if renumber := unusedArgs(used, len(args)); renumber != nil {
		if q, _, _, err = rewrite(query, true, renumber); err != nil {
			return "", nil, err
		}
		kept := make([]any, 0, len(renumber))
		for i, a := range args {
			if _, ok := renumber[i+1]; ok {
				kept = append(kept, a)
			}
		}
		args = kept
	}
	return q, postgresArgs(args), nil
}

// unusedArgs returns the renumbering (old index -> new index) that drops the
// arguments numbered placeholders do not reference, or nil when every
// argument is referenced (or the query has no numbered placeholders).
func unusedArgs(used map[int]bool, nargs int) map[int]int {
	if len(used) == 0 {
		return nil
	}
	all := true
	for i := 1; i <= nargs; i++ {
		all = all && used[i]
	}
	if all {
		return nil
	}
	idx := make([]int, 0, len(used))
	for i := range used {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	renumber := make(map[int]int, len(idx))
	for n, i := range idx {
		renumber[i] = n + 1
	}
	return renumber
}

// rewrite performs the placeholder (and optionally LIKE) translation. It
// returns the numbered placeholder indices seen and the number of plain
// placeholders; renumber, when non-nil, maps numbered indices to new ones.
func rewrite(query string, ilike bool, renumber map[int]int) (string, map[int]bool, int, error) {
	var b strings.Builder
	b.Grow(len(query) + 16)

	var (
		plain int
		used  map[int]bool
	)
	next := 1
	n := len(query)
	for i := 0; i < n; {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			// Literal or quoted identifier; a doubled quote is an escaped one.
			j := i + 1
			for j < n {
				if query[j] == c {
					if j+1 < n && query[j+1] == c {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(query[i:j])
			i = j
		case c == '-' && i+1 < n && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = n
			} else {
				j += i + 1
			}
			b.WriteString(query[i:j])
			i = j
		case c == '/' && i+1 < n && query[i+1] == '*':
			j := strings.Index(query[i+2:], "*/")
			if j < 0 {
				j = n
			} else {
				j += i + 4
			}
			b.WriteString(query[i:j])
			i = j
		case c == '?':
			j := i + 1
			for j < n && query[j] >= '0' && query[j] <= '9' {
				j++
			}
			b.WriteByte('$')
			if j > i+1 {
				num, err := strconv.Atoi(query[i+1 : j])
				if err != nil {
					return "", nil, 0, err
				}
				if used == nil {
					used = map[int]bool{}
				}
				used[num] = true
				if to, ok := renumber[num]; ok {
					num = to
				}
				b.WriteString(strconv.Itoa(num))
			} else {
				plain++
				b.WriteString(strconv.Itoa(next))
				next++
			}
			if plain > 0 && used != nil {
				return "", nil, 0, ErrMixedPlaceholders
			}
			i = j
		case ilike && (c == 'l' || c == 'L') && isKeywordAt(query, i, "like"):
			b.WriteString("ILIKE")
			i += 4
		case isIdentByte(c):
			// Copy a whole identifier so keywords are only matched as words.
			j := i + 1
			for j < n && isIdentByte(query[j]) {
				j++
			}
			b.WriteString(query[i:j])
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), used, plain, nil
}

// isKeywordAt reports whether the identifier starting at i is exactly kw
// (case-insensitive). i must be at the start of an identifier.
func isKeywordAt(s string, i int, kw string) bool {
	end := i + len(kw)
	if end > len(s) || !strings.EqualFold(s[i:end], kw) {
		return false
	}
	return end == len(s) || !isIdentByte(s[end])
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
