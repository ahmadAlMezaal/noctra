package sweep

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CronSchedule struct {
	minute, hour, dom, month, dow uint64
	domRestricted, dowRestricted  bool
}

func ParseCron(expr string) (*CronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q: expected 5 fields, got %d", expr, len(fields))
	}
	s := &CronSchedule{}
	var err error
	if s.minute, err = parseCronField(fields[0], 0, 59); err != nil {
		return nil, err
	}
	if s.hour, err = parseCronField(fields[1], 0, 23); err != nil {
		return nil, err
	}
	if s.dom, err = parseCronField(fields[2], 1, 31); err != nil {
		return nil, err
	}
	if s.month, err = parseCronField(fields[3], 1, 12); err != nil {
		return nil, err
	}
	if s.dow, err = parseCronField(fields[4], 0, 7); err != nil {
		return nil, err
	}
	if s.dow&(1<<7) != 0 {
		s.dow |= 1 << 0
		s.dow &^= 1 << 7
	}
	s.domRestricted = fields[2] != "*"
	s.dowRestricted = fields[4] != "*"
	return s, nil
}

func parseCronField(field string, min, max int) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(field, ",") {
		step := 1
		rng := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			rng = part[:i]
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 {
				return 0, fmt.Errorf("cron: invalid step in %q", part)
			}
			step = n
		}
		lo, hi := min, max
		if rng != "*" {
			if i := strings.IndexByte(rng, '-'); i >= 0 {
				a, err1 := strconv.Atoi(rng[:i])
				b, err2 := strconv.Atoi(rng[i+1:])
				if err1 != nil || err2 != nil {
					return 0, fmt.Errorf("cron: invalid range in %q", part)
				}
				lo, hi = a, b
			} else {
				v, err := strconv.Atoi(rng)
				if err != nil {
					return 0, fmt.Errorf("cron: invalid value %q", part)
				}
				lo, hi = v, v
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("cron: value out of range [%d-%d] in %q", min, max, part)
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, nil
}

func (s *CronSchedule) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 0)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if s.matches(t) {
			return t
		}
	}
	return time.Time{}
}

func (s *CronSchedule) matches(t time.Time) bool {
	if s.minute&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if s.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if s.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domMatch := s.dom&(1<<uint(t.Day())) != 0
	dowMatch := s.dow&(1<<uint(int(t.Weekday()))) != 0
	switch {
	case s.domRestricted && s.dowRestricted:
		return domMatch || dowMatch
	case s.domRestricted:
		return domMatch
	case s.dowRestricted:
		return dowMatch
	default:
		return true
	}
}
