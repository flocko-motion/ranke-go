package ranke

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ms(year, month, day int) int64 {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// msAt is ms with a time of day, for the spans an instant bounds.
func msAt(year, month, day, hour, min, sec int) int64 {
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC).UnixMilli()
}

// TestEDTFSpanValid pins the span each accepted form denotes, including the
// R-QTEMPORAL examples given verbatim in the rule text.
func TestEDTFSpanValid(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		start, end int64
	}{
		{"year", "2014", ms(2014, 1, 1), ms(2015, 1, 1)},
		{"year-month", "2014-06", ms(2014, 6, 1), ms(2014, 7, 1)},
		{"december rolls the year over", "2014-12", ms(2014, 12, 1), ms(2015, 1, 1)},
		{"year-month-day", "2014-06-11", ms(2014, 6, 11), ms(2014, 6, 12)},
		{"decade", "201X", ms(2010, 1, 1), ms(2020, 1, 1)},
		{"century", "20XX", ms(2000, 1, 1), ms(2100, 1, 1)},
		{"millennium", "2XXX", ms(2000, 1, 1), ms(3000, 1, 1)},
		{"negative year", "-1985", ms(-1985, 1, 1), ms(-1984, 1, 1)},
		{"season spring", "2014-21", ms(2014, 3, 1), ms(2014, 6, 1)},
		{"season winter crosses the year", "2014-24", ms(2014, 12, 1), ms(2015, 3, 1)},
		{"uncertain qualifier ignored", "2014?", ms(2014, 1, 1), ms(2015, 1, 1)},
		{"approximate qualifier ignored", "2014~", ms(2014, 1, 1), ms(2015, 1, 1)},
		{"uncertain-approximate qualifier ignored", "2014%", ms(2014, 1, 1), ms(2015, 1, 1)},
		{"closed interval", "2014/2016", ms(2014, 1, 1), ms(2017, 1, 1)},
		{"open start extends one year before the bound it faces", "..2005", ms(2004, 1, 1), ms(2006, 1, 1)},
		{"unknown start, same as open", "/2005", ms(2004, 1, 1), ms(2006, 1, 1)},
		{"open end extends one year past the bound it faces", "2020..", ms(2020, 1, 1), ms(2022, 1, 1)},
		{"unknown end, same as open", "2020/", ms(2020, 1, 1), ms(2022, 1, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, ok := edtfSpan(c.value)
			require.True(t, ok, "%q should parse", c.value)
			require.Equal(t, c.start, start, "start")
			require.Equal(t, c.end, end, "end")
		})
	}
}

// TestEDTFSpanDateTime covers EDTF's own Level 0 date-and-time form: `dated` is
// outside `V-TIME` (V-DATED), so a form V-TIME itself would refuse — no fraction, or
// a numeric offset rather than Z — is still a valid instant here.
func TestEDTFSpanDateTime(t *testing.T) {
	t.Run("no fractional seconds", func(t *testing.T) {
		start, end, ok := edtfSpan("1985-04-12T23:20:30Z")
		want := time.Date(1985, 4, 12, 23, 20, 30, 0, time.UTC).UnixMilli()
		require.True(t, ok)
		require.Equal(t, want, start)
		require.Equal(t, want, end, "an instant is a zero-width span")
	})
	t.Run("numeric offset normalises to UTC", func(t *testing.T) {
		start, end, ok := edtfSpan("2014-06-15T12:00:00+02:00")
		want := time.Date(2014, 6, 15, 10, 0, 0, 0, time.UTC).UnixMilli()
		require.True(t, ok)
		require.Equal(t, want, start)
		require.Equal(t, want, end)
	})
}

// TestEDTFDateTimeTakesTheLevel1Forms: an instant is an endpoint of the grammar like a
// date is, so the qualifiers and interval bounds Level 1 layers on top reach it too —
// the forms a caller wants when a real timestamp is uncertain or bounds a range.
func TestEDTFDateTimeTakesTheLevel1Forms(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		start, end int64
	}{
		{"uncertain", "2004-01-01T10:10:10Z?", msAt(2004, 1, 1, 10, 10, 10), msAt(2004, 1, 1, 10, 10, 10)},
		{"approximate, over an offset", "2004-01-01T10:10:10+05:00~", msAt(2004, 1, 1, 5, 10, 10), msAt(2004, 1, 1, 5, 10, 10)},
		{"interval between two instants", "2004-01-01T10:10:10Z/2004-01-01T18:00:00Z", msAt(2004, 1, 1, 10, 10, 10), msAt(2004, 1, 1, 18, 0, 0)},
		{"instant against a coarser bound", "2004-01-01T10:10:10Z/2004-06", msAt(2004, 1, 1, 10, 10, 10), ms(2004, 7, 1)},
		{"open end runs a year past the instant", "2004-01-01T10:10:10Z..", msAt(2004, 1, 1, 10, 10, 10), msAt(2005, 1, 1, 10, 10, 10)},
		{"unknown start runs a year before it", "/2004-01-01T10:10:10Z", msAt(2003, 1, 1, 10, 10, 10), msAt(2004, 1, 1, 10, 10, 10)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, ok := edtfSpan(c.value)
			require.True(t, ok, "%q should parse", c.value)
			require.Equal(t, c.start, start, "start")
			require.Equal(t, c.end, end, "end")
		})
	}
}

// TestEDTFSpanLetterPrefixedYear covers a year beyond 4 digits (`V-DATED`'s Level 1
// letter-prefixed calendar year), where the plain YYYY grammar cannot reach.
func TestEDTFSpanLetterPrefixedYear(t *testing.T) {
	start, end, ok := edtfSpan("Y12345")
	require.True(t, ok)
	require.Equal(t, ms(12345, 1, 1), start)
	require.Equal(t, ms(12346, 1, 1), end)

	start, end, ok = edtfSpan("Y-12345")
	require.True(t, ok)
	require.Equal(t, ms(-12345, 1, 1), start)
	require.Equal(t, ms(-12344, 1, 1), end)
}

// TestEDTFSpanInvalid: Level 2 (sets, non-rightmost/non-year unspecified digits) and
// plain garbage are refused — V-DATED pins `dated` to Level 1.
func TestEDTFSpanInvalid(t *testing.T) {
	for _, v := range []string{
		"", "whenever", "2014-13", "2014-00", "2014-06-32", "2014-06-00",
		"{2001,2002,2003}", "[2001,2002,2003]", // Level 2 sets
		"201X-01",              // unspecified digits only apply to the year alone
		"XXXX",                 // no fixed digit at all is not meaningful
		"../..",                // both bounds open faces nothing
		"2014/2010",            // a backwards interval
		"2014--06",             // malformed separator
		"2014-06-15T12:00:00",  // a date-time needs a zone (Z or offset) to be an instant
		"2014-06-15T12:00:00?", // and a qualifier does not supply one
		"2014-06-15T25:00:00Z", // an hour outside the clock
		"2014-21T12:00:00Z",    // a season is a quarter of a year, so no time of day falls in it
		"T12:00:00Z",           // a time of day names no moment without its date
		"2014-06-15T12:00:00Z/2014-06-15T12:00:00Z", // an interval spanning nothing
	} {
		t.Run(v, func(t *testing.T) {
			_, _, ok := edtfSpan(v)
			require.False(t, ok, "%q should be refused", v)
		})
	}
}

// TestEDTFMidpointOrdering pins the R-QTEMPORAL examples that compare two values
// directly: a plain year precedes an unspecified-digit year covering it (the decade
// centres later), and a value precedes the interval built from it.
func TestEDTFMidpointOrdering(t *testing.T) {
	yearMid, ok := edtfMidpointMs("2010")
	require.True(t, ok)
	decadeMid, ok := edtfMidpointMs("201X")
	require.True(t, ok)
	require.Less(t, yearMid, decadeMid, "2010 precedes 201X, whose decade centres five years later")

	plainMid, ok := edtfMidpointMs("2014")
	require.True(t, ok)
	intervalMid, ok := edtfMidpointMs("2014/2016")
	require.True(t, ok)
	require.Less(t, plainMid, intervalMid, "2014 precedes 2014/2016")
}

// TestEDTFMidpointTimestampSharesTheAxis: a timestamp (`V-TIME`) is a zero-width span,
// so it compares against an EDTF value on the same millisecond axis.
func TestEDTFMidpointTimestampSharesTheAxis(t *testing.T) {
	tsMid, ok := edtfMidpointMs("2014-06-15T12:00:00.000000000Z")
	require.True(t, ok)
	require.Equal(t, time.Date(2014, 6, 15, 12, 0, 0, 0, time.UTC).UnixMilli(), tsMid)
}

// TestEDTFMidpointInstantKeepsItsPlace: a qualified instant sits where the bare one
// does, and both fall inside the day holding them — sub-day precision reaches the axis
// rather than rounding to the date.
func TestEDTFMidpointInstantKeepsItsPlace(t *testing.T) {
	bare, ok := edtfMidpointMs("2004-01-01T10:10:10Z")
	require.True(t, ok)
	uncertain, ok := edtfMidpointMs("2004-01-01T10:10:10Z?")
	require.True(t, ok)
	require.Equal(t, bare, uncertain, "a qualifier doesn't move the midpoint")

	day, ok := edtfMidpointMs("2004-01-01")
	require.True(t, ok)
	require.Less(t, bare, day, "10:10 precedes the day's own midpoint at noon")
	require.Greater(t, bare, ms(2004, 1, 1), "and follows the day's start")
}

func TestValidateDated(t *testing.T) {
	require.NoError(t, validateDated("2014"))
	require.NoError(t, validateDated("2014-06-11"))
	require.NoError(t, validateDated("201X"))
	require.NoError(t, validateDated("2014/2016"))
	require.NoError(t, validateDated("2026-01-05T12:00:00.000000000Z"))
	// A commit's author date, offset and all, held as uncertain — the case EDTF's
	// time-bearing forms exist for.
	require.NoError(t, validateDated("2004-01-01T10:10:10+05:00?"))

	require.ErrorIs(t, validateDated("whenever"), ErrDatedForm)
	require.ErrorIs(t, validateDated("{2001,2002}"), ErrDatedForm)
}
