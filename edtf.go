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

// validateDated reports whether s is acceptable as a node's `dated`: an RFC 3339 instant,
// or an EDTF Level 1 value (`V-DATED`).
func validateDated(s string) error {
	if _, _, ok := edtfSpan(s); !ok {
		return WithDetail(ErrDatedForm, s)
	}
	return nil
}

// edtfSpan returns the half-open millisecond span [start, end) s denotes (`R-QTEMPORAL`);
// ok is false when s is neither an instant nor a valid EDTF Level 1 value.
func edtfSpan(s string) (start, end int64, ok bool) {
	// EDTF's own date-and-time form, wider than V-TIME's fixed-width created_at,
	// which `dated` is explicitly outside of (V-DATED).
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		ms := floorDivInt64(t.UnixNano(), int64(time.Millisecond))
		return ms, ms, true
	}
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

// TemporalMidpointMs is edtfMidpointMs, exported for a storage layer to project at write
// time — a native `compare: temporal` ORDER BY sorts on the projection rather than parsing
// EDTF itself (`R-QTEMPORAL`).
func TemporalMidpointMs(s string) (int64, bool) {
	return edtfMidpointMs(s)
}

// floorDivInt64 divides rounding toward negative infinity, unlike Go's truncating /: the
// span math needs this for dates before 1970, whose ms values are negative.
func floorDivInt64(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// edtfPoint is one parsed EDTF Level 1 endpoint, exactly one shape populated: a
// season, an unspecified-digit year (spanYears > 0), or a year with an optional
// month and day.
type edtfPoint struct {
	year      int
	hasMonth  bool
	month     int
	hasDay    bool
	day       int
	season    int // 21-24; 0 when this point is not a season
	spanYears int // > 0 for an unspecified-digit year (10^(number of trailing X's))
}

// parseEDTFLevel1 parses a full `dated` value: a single point, an A/B interval whose either
// side may be empty (unknown) or `..` (open), or R-QTEMPORAL's own shorthand for an open
// bound written directly against the concrete side with no `/` at all (`..2005`, `2020..`).
// Either open form extends one calendar year past the edge of the concrete bound it faces.
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

// parseEDTFSlashInterval parses the ISO 8601-2 A/B form, either side empty (unknown) or
// `..` (open).
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

// parseEDTFPoint parses one endpoint: a trailing `?`/`~`/`%` qualifier is accepted and
// dropped (span/midpoint math ignores it — R-QTEMPORAL's tie-break on it is out of scope),
// then a letter-prefixed year (`Y170000002`), an unspecified-digit year (`201X`, `20XX`,
// `2XXX`), or `year[-month[-day]]`, month 21-24 read as a season.
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

// parseUnspecifiedYear reads a 4-character year with 1 to 3 trailing 'X's (`201X` the
// decade, `20XX` the century, `2XXX` the millennium); ok is false for a plain year (0 X's)
// or one masked past its first digit.
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

// pointSpanMs is p's own half-open millisecond span, the precision it carries: a year
// covers its year, a season its three months, a day just that day.
func pointSpanMs(p edtfPoint) (int64, int64) {
	switch {
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

// timeShiftYearsMs shifts a millisecond edge by whole calendar years — the "one year"
// R-QTEMPORAL extends an open or unknown interval bound by, leap years included.
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
