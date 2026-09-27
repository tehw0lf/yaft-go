package yaft

import (
	"log/slog"
	"regexp"
	"strconv"
	"time"
)

// Feature is a feature toggle as the backend stores it.
//
// Value is a string, not a bool: only the exact string "true" switches a
// feature on (R1, R4). ActiveAt and DisabledAt are optional RFC 3339 bounds;
// an empty string means "no bound" (R2), and so does a JSON null, which
// decodes to "".
type Feature struct {
	Key        string   `json:"key"`
	Value      string   `json:"value"`
	ActiveAt   string   `json:"activeAt"`
	DisabledAt string   `json:"disabledAt"`
	Tags       []string `json:"tags"`
}

// Clock returns the current time. Everything that evaluates a feature takes
// one instead of calling time.Now, so tests -- and the conformance suite,
// which supplies a "now" with every case -- can evaluate at a fixed instant
// (R9). A nil Clock means time.Now.
type Clock func() time.Time

func (c Clock) now() time.Time {
	if c == nil {
		return time.Now()
	}
	return c()
}

// rfc3339WithOffset matches RFC 3339 with an explicit offset. A bare date or a
// timestamp without an offset is rejected: languages disagree on how to read
// them, so a feature would flip at a different instant in each port (R10).
// \d matches ASCII digits only in Go, as in JavaScript.
var rfc3339WithOffset = regexp.MustCompile(
	`^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(?:([Zz])|([+-])(\d{2}):(\d{2}))$`)

// Evaluate decides whether feature is on at the instant now. It is the single
// definition of YaFT's evaluation rules; providers call it rather than
// implementing them.
//
//   - A nil feature is off.
//   - Only the exact string "true" is on.
//   - Unset or unparseable bounds are ignored, never an error.
//   - The window is half-open, [ActiveAt, DisabledAt): at exactly ActiveAt the
//     feature is on, at exactly DisabledAt it is off.
//   - ActiveAt after DisabledAt is not special-cased; the window never opens.
func Evaluate(feature *Feature, now time.Time) bool {
	if feature == nil || feature.Value != "true" {
		return false
	}
	if activeAt, ok := ParseTimestamp(feature.ActiveAt); ok && now.Before(activeAt) {
		return false
	}
	if disabledAt, ok := ParseTimestamp(feature.DisabledAt); ok && !now.Before(disabledAt) {
		return false
	}
	return true
}

// ParseTimestamp parses an RFC 3339 timestamp with an offset. It reports false
// for anything unset, malformed, out of range or in another format; callers
// treat that as "no bound". A malformed value is logged, never an error.
//
// The calendar is range-checked before anything is built (R11) -- time.Date
// would otherwise normalise 2027-02-30 into March. A leap second is ignored
// (R12). Fractional seconds are truncated to milliseconds (R27), and the
// offset may range up to ±23:59 (R28).
func ParseTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	m := rfc3339WithOffset.FindStringSubmatch(value)
	if m == nil {
		return ignore(value, "expected RFC 3339 with an offset (e.g. 2026-09-18T15:00:00Z)")
	}

	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, minute, second := atoi(m[4]), atoi(m[5]), atoi(m[6])
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) || hour > 23 || minute > 59 || second > 60 {
		return ignore(value, "not a valid date or time")
	}
	if second == 60 {
		return ignore(value, "leap seconds are not supported")
	}

	offset := 0
	if m[8] == "" {
		hours, minutes := atoi(m[10]), atoi(m[11])
		if hours > 23 || minutes > 59 {
			return ignore(value, "not a valid offset")
		}
		offset = hours*3600 + minutes*60
		if m[9] == "-" {
			offset = -offset
		}
	}

	millis := 0
	if fraction := m[7]; fraction != "" {
		millis = atoi((fraction + "00")[:3])
	}

	// FixedZone takes any offset, so the ±18:00 limit some libraries have
	// does not apply; the offset is applied, not stripped (R13).
	zone := time.FixedZone("", offset)
	return time.Date(year, time.Month(month), day, hour, minute, second, millis*int(time.Millisecond), zone).UTC(), true
}

func daysIn(year, month int) int {
	// Day 0 of the next month is the last day of this one.
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func atoi(digits string) int {
	n, _ := strconv.Atoi(digits) // the pattern guarantees ASCII digits
	return n
}

func ignore(value, reason string) (time.Time, bool) {
	slog.Warn("yaft: ignoring timestamp", "value", value, "reason", reason)
	return time.Time{}, false
}
