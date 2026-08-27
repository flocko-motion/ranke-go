package ranke

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ms(year, month, day int) int64 {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli()
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
		"201X-01",   // unspecified digits only apply to the year alone
		"XXXX",      // no fixed digit at all is not meaningful
		"../..",     // both bounds open faces nothing
		"2014/2010", // a backwards interval
		"2014--06",  // malformed separator
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

func TestValidateDated(t *testing.T) {
	require.NoError(t, validateDated("2014"))
	require.NoError(t, validateDated("2014-06-11"))
	require.NoError(t, validateDated("201X"))
	require.NoError(t, validateDated("2014/2016"))
	require.NoError(t, validateDated("2026-01-05T12:00:00.000000000Z"))

	require.ErrorIs(t, validateDated("whenever"), ErrDatedForm)
	require.ErrorIs(t, validateDated("{2001,2002}"), ErrDatedForm)
}
