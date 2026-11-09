package schedule

import (
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	out, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCronDSTAndTimezone(t *testing.T) {
	for _, tc := range []struct{ name, expression, zone, start, want, following string }{
		{"spring gap", "30 2 * * *", "America/New_York", "2026-03-08T00:00:00Z", "2026-03-09T06:30:00Z", "2026-03-10T06:30:00Z"},
		{"fall repeat", "30 1 * * *", "America/New_York", "2026-11-01T00:00:00Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
		{"quarter hour zone", "0 0 * * *", "Asia/Kathmandu", "2026-01-01T00:00:00Z", "2026-01-01T18:15:00Z", "2026-01-02T18:15:00Z"},
		{"inclusive start", "*/15 9-10 * * 1-5", "UTC", "2026-11-06T09:00:00Z", "2026-11-06T09:00:00Z", "2026-11-06T09:15:00Z"},
		{"day OR weekday", "0 0 1 * 0", "UTC", "2026-11-02T00:00:00Z", "2026-11-08T00:00:00Z", "2026-11-15T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Definition{Kind: "cron", Cron: tc.expression, Timezone: tc.zone, StartAt: instant(t, tc.start)}
			first, err := d.First()
			if err != nil || first == nil || !first.Equal(instant(t, tc.want)) {
				t.Fatalf("first=%v err=%v", first, err)
			}
			next, err := d.Next(*first)
			if err != nil || next == nil || !next.Equal(instant(t, tc.following)) {
				t.Fatalf("next=%v err=%v", next, err)
			}
		})
	}
}

func TestInvalidCron(t *testing.T) {
	for _, expr := range []string{"", "@daily", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "1-0 * * * *", "1,,2 * * * *", "a * * * *", "* * * * -1"} {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("accepted %q", expr)
		}
	}
	d := Definition{Kind: "cron", Cron: "0 0 31 2 *", Timezone: "UTC", StartAt: instant(t, "2026-01-01T00:00:00Z")}
	if _, err := d.First(); err == nil {
		t.Fatal("accepted impossible date")
	}
}

func TestCronDomStepCombinesWithWeekdayOR(t *testing.T) {
	// Only a bare * is an unconstrained day field; */n is still specified and ORs with dow.
	start := instant(t, "2026-02-01T12:00:00Z")
	d := Definition{Kind: "cron", Cron: "0 0 */5 * 0", Timezone: "UTC", StartAt: start}
	first, err := d.First()
	if err != nil || first == nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	want := instant(t, "2026-02-06T00:00:00Z") // day 6 matches */5; treating */5 as * wrongly ANDed with Sunday until mid-February.
	if !first.Equal(want) {
		t.Fatalf("first=%v want=%v", first, want)
	}
	next, err := d.Next(*first)
	if err != nil || next == nil {
		t.Fatalf("next=%v err=%v", next, err)
	}
	wantNext := instant(t, "2026-02-08T00:00:00Z") // Sunday matches the restricted weekday field.
	if !next.Equal(wantNext) {
		t.Fatalf("next=%v want=%v", next, wantNext)
	}
}

func TestIntervalUsesAbsoluteAnchor(t *testing.T) {
	d := Definition{Kind: "interval", StartAt: instant(t, "2026-03-08T01:30:00.5-05:00"), IntervalSeconds: 3600}
	for _, tc := range []struct{ after, want string }{
		{"2026-03-08T06:30:00.5Z", "2026-03-08T07:30:00.5Z"},
		{"2026-03-08T07:30:00Z", "2026-03-08T07:30:00.5Z"},
		{"2026-03-08T09:40:00Z", "2026-03-08T10:30:00.5Z"},
	} {
		next, err := d.Next(instant(t, tc.after))
		if err != nil || next == nil || !next.Equal(instant(t, tc.want)) {
			t.Fatalf("next=%v err=%v want=%s", next, err, tc.want)
		}
	}
}
