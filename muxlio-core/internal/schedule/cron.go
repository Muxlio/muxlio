package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cron struct {
	fields          [5]map[int]bool
	dayAny, weekAny bool
}

// Five numeric fields support *, lists, inclusive ranges, and /steps.
// Restricted day-of-month and day-of-week fields are combined with OR.
func parseCron(expression string) (*cron, error) {
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron requires five numeric fields")
	}
	c := &cron{dayAny: parts[2] == "*", weekAny: parts[4] == "*"}
	mins := [5]int{0, 0, 1, 1, 0}
	maxs := [5]int{59, 23, 31, 12, 7}
	for i, part := range parts {
		c.fields[i] = map[int]bool{}
		for _, item := range strings.Split(part, ",") {
			stepParts := strings.Split(item, "/")
			if len(stepParts) > 2 {
				return nil, fmt.Errorf("invalid cron field %q", part)
			}
			step := 1
			if len(stepParts) == 2 {
				var err error
				step, err = strconv.Atoi(stepParts[1])
				if err != nil || step < 1 || step > maxs[i]+1 {
					return nil, fmt.Errorf("invalid cron step")
				}
			}
			lo, hi := mins[i], maxs[i]
			if stepParts[0] != "*" {
				bounds := strings.Split(stepParts[0], "-")
				if len(bounds) > 2 {
					return nil, fmt.Errorf("invalid cron range")
				}
				var err error
				lo, err = strconv.Atoi(bounds[0])
				if err != nil {
					return nil, fmt.Errorf("invalid cron number")
				}
				hi = lo
				if len(bounds) == 2 {
					hi, err = strconv.Atoi(bounds[1])
					if err != nil {
						return nil, fmt.Errorf("invalid cron number")
					}
				} else if len(stepParts) == 2 {
					hi = maxs[i]
				}
			}
			if lo < mins[i] || hi > maxs[i] || lo > hi {
				return nil, fmt.Errorf("cron field out of range")
			}
			for v := lo; v <= hi; v += step {
				key := v
				if i == 4 && key == 7 {
					key = 0
				}
				c.fields[i][key] = true
			}
		}
	}
	return c, nil
}

func (c *cron) dateMatches(t time.Time) bool {
	day, week := c.fields[2][t.Day()], c.fields[4][int(t.Weekday())]
	match := day || week
	if c.dayAny || c.weekAny {
		match = day && week
	}
	return c.fields[3][int(t.Month())] && match
}

func (c *cron) matches(t time.Time) bool {
	return c.fields[0][t.Minute()] && c.fields[1][t.Hour()] && c.dateMatches(t)
}
