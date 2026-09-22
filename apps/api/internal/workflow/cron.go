package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A five-field cron expression, in UTC, exactly as GitHub Actions' `schedule:`
// takes one:
//
//	┌ minute (0-59)
//	│ ┌ hour (0-23)
//	│ │ ┌ day of month (1-31)
//	│ │ │ ┌ month (1-12)
//	│ │ │ │ ┌ day of week (0-6, Sunday = 0)
//	* * * * *
//
// Supported per field: `*`, a number, a list `a,b`, a range `a-b`, and a step
// `*/n` or `a-b/n`. That is the whole of what Actions documents, and no more —
// no `@daily`, no seconds field, no `L`/`W`/`#`. A cron expression that means
// something here and something else in a repository's own Actions file would be
// a trap, so the answer is to support the same grammar and reject the rest.
//
// UTC, always. A schedule that shifts twice a year depending on where the server
// is deployed is a bug waiting for a daylight-saving boundary; Actions made the
// same choice for the same reason.

type cronField struct {
	min, max int
	name     string
}

var cronFields = []cronField{
	{0, 59, "minute"},
	{0, 23, "hour"},
	{1, 31, "day of month"},
	{1, 12, "month"},
	{0, 6, "day of week"},
}

// Cron is a parsed expression: one set of permitted values per field.
type Cron struct {
	allowed [5]map[int]bool
	// domRestricted and dowRestricted record whether the day-of-month and
	// day-of-week fields were narrowed. Cron's oddest rule depends on it (see
	// Matches).
	domRestricted, dowRestricted bool
	expr                         string
}

// ParseCron parses a five-field expression.
func ParseCron(expr string) (*Cron, error) {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) != 5 {
		return nil, fmt.Errorf("a cron expression has 5 fields (minute hour day-of-month month day-of-week), got %d in %q",
			len(parts), expr)
	}
	c := &Cron{expr: strings.TrimSpace(expr)}
	for i, part := range parts {
		set, err := parseField(part, cronFields[i])
		if err != nil {
			return nil, err
		}
		c.allowed[i] = set
		if part != "*" {
			switch i {
			case 2:
				c.domRestricted = true
			case 4:
				c.dowRestricted = true
			}
		}
	}
	return c, nil
}

// ValidateCron reports whether an expression parses, for load-time validation.
func ValidateCron(expr string) error {
	_, err := ParseCron(expr)
	return err
}

func parseField(spec string, f cronField) (map[int]bool, error) {
	out := map[int]bool{}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("%s: empty item in %q", f.name, spec)
		}
		step := 1
		if slash := strings.Index(item, "/"); slash >= 0 {
			n, err := strconv.Atoi(item[slash+1:])
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%s: step must be a positive number, got %q", f.name, item)
			}
			step = n
			item = item[:slash]
		}
		lo, hi := f.min, f.max
		switch {
		case item == "*":
		case strings.Contains(item, "-"):
			bounds := strings.SplitN(item, "-", 2)
			var err error
			if lo, err = strconv.Atoi(strings.TrimSpace(bounds[0])); err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", f.name, bounds[0])
			}
			if hi, err = strconv.Atoi(strings.TrimSpace(bounds[1])); err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", f.name, bounds[1])
			}
			if lo > hi {
				return nil, fmt.Errorf("%s: range %q runs backwards", f.name, item)
			}
		default:
			n, err := strconv.Atoi(item)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number, a range or *", f.name, item)
			}
			lo, hi = n, n
		}
		if lo < f.min || hi > f.max {
			return nil, fmt.Errorf("%s must be %d-%d, got %q", f.name, f.min, f.max, spec)
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: %q matches nothing", f.name, spec)
	}
	return out, nil
}

// Matches reports whether t (interpreted in UTC) satisfies the expression.
//
// The day rule is cron's least obvious and is implemented the way every cron
// implements it: when BOTH day-of-month and day-of-week are restricted, a day
// matching EITHER runs. `0 0 1 * 1` is the 1st of the month and every Monday,
// not Mondays that fall on the 1st. Getting this wrong is silent — the schedule
// simply fires less often than intended — so it is spelled out here.
func (c *Cron) Matches(t time.Time) bool {
	t = t.UTC()
	if !c.allowed[0][t.Minute()] || !c.allowed[1][t.Hour()] || !c.allowed[3][int(t.Month())] {
		return false
	}
	dom, dow := c.allowed[2][t.Day()], c.allowed[4][int(t.Weekday())]
	if c.domRestricted && c.dowRestricted {
		return dom || dow
	}
	return dom && dow
}

// Next is the first matching minute strictly after t, or the zero time if none
// falls within four years (a bound rather than an unbounded scan: `0 0 30 2 *`
// — the 30th of February — never matches, and a scheduler must not hang on it).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 0)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if c.Matches(t) {
			return t
		}
	}
	return time.Time{}
}

func (c *Cron) String() string { return c.expr }
