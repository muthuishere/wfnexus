package workflow

import (
	"testing"
	"time"
)

func TestCronGrammarMatchesActions(t *testing.T) {
	ok := []string{
		"* * * * *", "0 9 * * 1", "*/15 * * * *", "0 0 1 * *",
		"30 2-4 * * *", "0 0 * * 0", "0,30 9-17 * * 1-5", "0 0 1 1 *", "0 6-18/6 * * *",
	}
	for _, e := range ok {
		if err := ValidateCron(e); err != nil {
			t.Errorf("%q should parse: %v", e, err)
		}
	}
	// Rejected on purpose: each of these means something in SOME cron dialect,
	// and accepting it here while a repository's own Actions file rejects it
	// would be the trap.
	bad := []string{
		"", "* * * *", "* * * * * *", "@daily", "0 0 * * MON",
		"60 * * * *", "* 24 * * *", "0 0 0 * *", "0 0 * 13 *", "0 0 * * 7",
		"*/0 * * * *", "5-1 * * * *", "x * * * *",
	}
	for _, e := range bad {
		if err := ValidateCron(e); err == nil {
			t.Errorf("%q should be refused", e)
		}
	}
}

// Cron's least obvious rule: with BOTH day fields restricted, a day matching
// EITHER runs. Getting it wrong is silent — the schedule just fires less often
// than the author intended.
func TestCronDayOfMonthOrDayOfWeek(t *testing.T) {
	c, err := ParseCron("0 0 1 * 1") // the 1st, AND every Monday
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-01 is a Tuesday: matches by day-of-month alone.
	if !c.Matches(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("the 1st should match")
	}
	// 2026-09-07 is a Monday: matches by day-of-week alone.
	if !c.Matches(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)) {
		t.Error("a Monday should match")
	}
	// 2026-09-02, a Wednesday, matches neither.
	if c.Matches(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)) {
		t.Error("a non-Monday that is not the 1st must not match")
	}

	// With only ONE day field restricted the two are ANDed as usual.
	c2, _ := ParseCron("0 0 * * 1")
	if c2.Matches(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("Tuesday matched a Monday-only schedule")
	}
}

// UTC always, so a schedule does not shift with the server's timezone.
func TestCronIsUTC(t *testing.T) {
	c, _ := ParseCron("0 9 * * *")
	plus8 := time.FixedZone("UTC+8", 8*3600)
	// 17:00 in UTC+8 is 09:00 UTC, so it matches despite the local hour.
	if !c.Matches(time.Date(2026, 9, 22, 17, 0, 0, 0, plus8)) {
		t.Error("a 09:00 UTC schedule must match 17:00 UTC+8")
	}
	if c.Matches(time.Date(2026, 9, 22, 9, 0, 0, 0, plus8)) {
		t.Error("09:00 local is 01:00 UTC and must not match")
	}
}

func TestCronNext(t *testing.T) {
	c, _ := ParseCron("*/15 * * * *")
	got := c.Next(time.Date(2026, 9, 22, 10, 7, 30, 0, time.UTC))
	want := time.Date(2026, 9, 22, 10, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
	// Strictly after: a time that already matches advances.
	if got := c.Next(want); !got.Equal(want.Add(15 * time.Minute)) {
		t.Fatalf("Next on a matching minute = %s", got)
	}
	// An expression that can never match must terminate, not hang.
	feb30, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if n := feb30.Next(time.Now()); !n.IsZero() {
		t.Fatalf("the 30th of February resolved to %s", n)
	}
}
