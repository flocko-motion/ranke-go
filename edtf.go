// package: ranke / taxonomy
// type:    logic
// job:     EDTF Level 1 parsing for `dated` (`V-DATED`), including its own date-and-time
// form, and the millisecond span/midpoint `R-QTEMPORAL`'s `compare: temporal` reads off it —
// an instant shares the same axis as a zero-width span
// limits:  Level 1 only: no sets, no individual-component qualification, no exponential years
package ranke

import (
	"strconv"
	"strings"
	"time"
)

// validateDated reports whether s is acceptable as a node's `dated` (`V-DATED`).
func validateDated(s string) error {
	if _, _, ok := edtfSpan(s); !ok {
		return WithDetail(ErrDatedForm, s)
	}
	return nil
}

// edtfSpan is the half-open millisecond span [start, end) s denotes (`R-QTEMPORAL`).
func edtfSpan(s string) (start, end int64, ok bool) {
	return parseEDTFLevel1(s)
}

// edtfMidpointMs is edtfSpan's midpoint, rounded down (`R-QTEMPORAL`).
func edtfMidpointMs(s string) (int64, bool) {
	start, end, ok := edtfSpan(s)
	if !ok {
		return 0, false
	}
	return floorDivInt64(start+end, 2), true
}

// TemporalMidpointMs is edtfMidpointMs, exported so a storage layer can project `dated` at
// write time and sort on the projection natively (`R-QTEMPORAL`).
func TemporalMidpointMs(s string) (int64, bool) {
	return edtfMidpointMs(s)
}

// floorDivInt64 rounds toward negative infinity, as the span math must for dates before 1970.
func floorDivInt64(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// edtfPoint is one parsed EDTF Level 1 endpoint, with exactly one shape populated.
type edtfPoint struct {
	year      int
	hasMonth  bool
	month     int
	hasDay    bool
	day       int
	season    int   // 21-24 for a season
	spanYears int   // an unspecified-digit year's width, 10^(number of trailing X's)
	instantMs int64 // the moment a date-and-time point names, on the UTC axis
	isInstant bool
}

// parseEDTFLevel1 parses a full `dated` value: one point, or an interval whose either side
// may be open — the `/` form or R-QTEMPORAL's bare `..2005`, each open bound a year wide.
func parseEDTFLevel1(s string) (start, end int64, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	if strings.Contains(s, "/") {
		return parseEDTFSlashInterval(s)
	}
	switch {
	case strings.HasPrefix(s, "..") && len(s) > 2:
		rp, pok := parseEDTFPoint(s[2:])
		if !pok {
			return 0, 0, false
		}
		rs, re := pointSpanMs(rp)
		return timeShiftYearsMs(rs, -1), re, true
	case strings.HasSuffix(s, "..") && len(s) > 2:
		lp, pok := parseEDTFPoint(s[:len(s)-2])
		if !pok {
			return 0, 0, false
		}
		ls, le := pointSpanMs(lp)
		return ls, timeShiftYearsMs(le, 1), true
	}
	p, pok := parseEDTFPoint(s)
	if !pok {
		return 0, 0, false
	}
	start, end = pointSpanMs(p)
	return start, end, true
}

// parseEDTFSlashInterval parses the ISO 8601-2 A/B form, either side empty (unknown) or `..`.
func parseEDTFSlashInterval(s string) (start, end int64, ok bool) {
	left, right, _ := strings.Cut(s, "/")
	openLeft := left == "" || left == ".."
	openRight := right == "" || right == ".."
	switch {
	case openLeft && openRight: // neither side has anything for the other to face
		return 0, 0, false
	case openLeft:
		rp, pok := parseEDTFPoint(right)
		if !pok {
			return 0, 0, false
		}
		rs, re := pointSpanMs(rp)
		start, end = timeShiftYearsMs(rs, -1), re
	case openRight:
		lp, pok := parseEDTFPoint(left)
		if !pok {
			return 0, 0, false
		}
		ls, le := pointSpanMs(lp)
		start, end = ls, timeShiftYearsMs(le, 1)
	default: // both concrete
		lp, lok := parseEDTFPoint(left)
		rp, rok := parseEDTFPoint(right)
		if !lok || !rok {
			return 0, 0, false
		}
		start, _ = pointSpanMs(lp)
		_, end = pointSpanMs(rp)
	}
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// parseEDTFPoint parses one endpoint, dropping a trailing `?`/`~`/`%` qualifier: a
// letter-prefixed year, a date and time of day, an unspecified-digit year, or
// `year[-month[-day]]` with month 21-24 read as a season.
func parseEDTFPoint(s string) (edtfPoint, bool) {
	if s == "" {
		return edtfPoint{}, false
	}
	switch s[len(s)-1] {
	case '?', '~', '%':
		s = s[:len(s)-1]
	}
	if s == "" {
		return edtfPoint{}, false
	}
	if s[0] == 'Y' {
		year, ok := parseSignedInt(s[1:])
		if !ok {
			return edtfPoint{}, false
		}
		return edtfPoint{year: year}, true
	}
	if strings.ContainsRune(s, 'T') {
		return parseEDTFInstant(s)
	}
	if len(s) == 4 {
		if p, ok := parseUnspecifiedYear(s); ok {
			return p, true
		}
	}
	neg, rest := false, s
	if rest[0] == '-' {
		neg, rest = true, rest[1:]
	}
	parts := strings.Split(rest, "-")
	if len(parts[0]) != 4 || !allDigits(parts[0]) {
		return edtfPoint{}, false
	}
	year, _ := strconv.Atoi(parts[0])
	if neg {
		year = -year
	}
	switch len(parts) {
	case 1:
		return edtfPoint{year: year}, true
	case 2:
		mm, ok := parseTwoDigits(parts[1])
		if !ok {
			return edtfPoint{}, false
		}
		if mm >= 21 && mm <= 24 {
			return edtfPoint{year: year, season: mm}, true
		}
		if mm < 1 || mm > 12 {
			return edtfPoint{}, false
		}
		return edtfPoint{year: year, hasMonth: true, month: mm}, true
	case 3:
		mm, ok := parseTwoDigits(parts[1])
		if !ok || mm < 1 || mm > 12 {
			return edtfPoint{}, false
		}
		dd, ok := parseTwoDigits(parts[2])
		if !ok || dd < 1 || dd > 31 {
			return edtfPoint{}, false
		}
		return edtfPoint{year: year, hasMonth: true, month: mm, hasDay: true, day: dd}, true
	default:
		return edtfPoint{}, false
	}
}

// parseEDTFInstant reads EDTF's date-and-time form, wider than V-TIME's fixed-width
// created_at: any second precision, and a zone, which is what fixes the moment.
func parseEDTFInstant(s string) (edtfPoint, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return edtfPoint{}, false
	}
	ms := floorDivInt64(t.UnixNano(), int64(time.Millisecond))
	return edtfPoint{instantMs: ms, isInstant: true}, true
}

// parseUnspecifiedYear reads a 4-character year with 1 to 3 trailing 'X's: `201X` the decade,
// `20XX` the century, `2XXX` the millennium.
func parseUnspecifiedYear(s string) (edtfPoint, bool) {
	x := 0
	for x < len(s) && s[len(s)-1-x] == 'X' {
		x++
	}
	if x == 0 || x >= len(s) {
		return edtfPoint{}, false
	}
	digits := s[:len(s)-x]
	if !allDigits(digits) {
		return edtfPoint{}, false
	}
	base, _ := strconv.Atoi(digits)
	mult := pow10(x)
	return edtfPoint{year: base * mult, spanYears: mult}, true
}

// pointSpanMs is p's own half-open millisecond span, the precision it carries: a year covers
// its year, a season three months, a day that day, an instant no width at all.
func pointSpanMs(p edtfPoint) (int64, int64) {
	switch {
	case p.isInstant:
		return p.instantMs, p.instantMs
	case p.spanYears > 0:
		return dateMs(p.year, 1, 1), dateMs(p.year+p.spanYears, 1, 1)
	case p.season != 0:
		switch p.season {
		case 21:
			return dateMs(p.year, 3, 1), dateMs(p.year, 6, 1)
		case 22:
			return dateMs(p.year, 6, 1), dateMs(p.year, 9, 1)
		case 23:
			return dateMs(p.year, 9, 1), dateMs(p.year, 12, 1)
		default: // 24: December through February, crossing into year+1
			return dateMs(p.year, 12, 1), dateMs(p.year+1, 3, 1)
		}
	case p.hasDay:
		start := dateMs(p.year, p.month, p.day)
		return start, timeShiftDaysMs(start, 1)
	case p.hasMonth:
		return dateMs(p.year, p.month, 1), dateMs(p.year, p.month+1, 1)
	default:
		return dateMs(p.year, 1, 1), dateMs(p.year+1, 1, 1)
	}
}

func dateMs(year, month, day int) int64 {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli()
}

func timeShiftDaysMs(ms int64, days int) int64 {
	return time.UnixMilli(ms).UTC().AddDate(0, 0, days).UnixMilli()
}

// timeShiftYearsMs shifts by whole calendar years, the width R-QTEMPORAL gives an open bound.
func timeShiftYearsMs(ms int64, years int) int64 {
	return time.UnixMilli(ms).UTC().AddDate(years, 0, 0).UnixMilli()
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseTwoDigits(s string) (int, bool) {
	if len(s) != 2 || !allDigits(s) {
		return 0, false
	}
	n, _ := strconv.Atoi(s)
	return n, true
}

func parseSignedInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	neg := false
	if s[0] == '-' {
		neg, s = true, s[1:]
	}
	if !allDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func pow10(n int) int {
	p := 1
	for range n {
		p *= 10
	}
	return p
}
