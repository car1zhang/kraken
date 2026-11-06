package jobs

import (
	"sync"
	"time"
)

// awakeClock answers how long the machine has been continuously awake, read
// off the daemon's own ticks. A sleeping Mac delivers no ticks; a dark wake
// (the maintenance wake macOS takes every fifteen minutes or so while a
// laptop sleeps, and in clusters of short wakes a few seconds apart on
// battery) delivers a handful of ticks and sleeps again. Each tick is read
// against the previous one on two clocks: the wall clock, which keeps
// running while the machine sleeps, and Go's monotonic reading, which on
// macOS does not (the darwin runtime reads mach_absolute_time, which stops
// during sleep; a Go release that changed that clock would make every gap
// read as awake, and this is the comment to revisit). The difference is the
// time the machine slept between the two ticks, and any sleep longer than
// sleepSlack restarts the awake span. Tick spacing on its own cannot tell: a
// cluster of dark wakes keeps ticks landing under a minute apart on the wall
// clock while most of that minute was spent asleep, and a slow tick
// stretches the spacing with no sleep at all.
//
// A clock that has never been fed holds no evidence either way and answers
// awakeUnknown, which every reader treats as "no reason to hold".
type awakeClock struct {
	mu    sync.Mutex
	since int64     // unix millis of the first tick after the last sleep
	last  int64     // unix millis of the last tick
	prev  time.Time // the last tick's reading, monotonic part included
}

// sleepSlack is how much of a tick-to-tick gap may be unaccounted for by the
// monotonic clock before the gap counts as sleep: the two readings are taken
// a few microseconds apart, never seconds.
const sleepSlack = 2 * time.Second

// sleepGap is the fallback rule for readings with no monotonic part, which
// only a test's hand-built time carries: a tick-to-tick gap longer than this
// reads as sleep. Four of the daemon's 15s ticks, well short of the
// dark-wake cycle.
const sleepGap = time.Minute

// awakeUnknown is awakeClock.For's answer when the clock has never been fed.
const awakeUnknown = time.Duration(-1)

// hasMonotonic reports whether t carries a monotonic clock reading. Round(0)
// strips the reading and nothing else, so the stripped value differs from
// the original exactly when there was one to strip.
func hasMonotonic(t time.Time) bool { return t != t.Round(0) }

// Note records one tick at now. The first tick opens the span; every later
// one is read against the previous tick by step.
func (c *awakeClock) Note(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := now.UnixMilli()
	if c.since == 0 {
		c.since, c.last, c.prev = ms, ms, now
		return
	}
	wall := ms - c.last
	awake := wall
	mono := hasMonotonic(now) && hasMonotonic(c.prev)
	if mono {
		awake = now.Sub(c.prev).Milliseconds() // monotonic, since both readings carry one
	}
	c.step(ms, wall, awake, mono)
	c.prev = now
}

// step is Note's arithmetic, split out so a test can describe a tick as "W
// wall milliseconds since the last one, A of them awake": with monotonic
// readings the span restarts when the sleep (wall minus awake) exceeds
// sleepSlack; without them it restarts when the wall gap exceeds sleepGap.
// A wall clock that moved backwards restarts the span either way.
func (c *awakeClock) step(ms, wall, awake int64, mono bool) {
	restart := wall < 0
	if mono {
		restart = restart || wall-awake > sleepSlack.Milliseconds()
	} else {
		restart = restart || wall > sleepGap.Milliseconds()
	}
	if restart {
		c.since = ms
	}
	c.last = ms
}

// For is how long the machine has been continuously awake as of now, or
// awakeUnknown when nothing has been recorded yet.
func (c *awakeClock) For(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.since == 0 {
		return awakeUnknown
	}
	d := time.Duration(now.UnixMilli()-c.since) * time.Millisecond
	if d < 0 {
		return 0
	}
	return d
}
