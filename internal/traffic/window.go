package traffic

import "time"

// windowHours is how many hourly slots a window keeps: the hour in progress
// plus the 24 before it. "Last 24h" therefore covers between 24 and 25 hours,
// which is the price of not keeping a timestamp per event.
const windowHours = 25

// Window is a counter with a running total and a rolling last-day figure.
//
// The zero value is an empty window. It is not safe for concurrent use.
type Window struct {
	total int64
	slots [windowHours]int64
	hours [windowHours]int64 // the hour each slot currently holds
}

// Add counts n at the given moment.
func (w *Window) Add(n int64, now time.Time) {
	hour := hourOf(now)
	slot := slotOf(hour)

	// A slot is reused every 25 hours and cleared only then, so an idle server
	// does no housekeeping at all.
	if w.hours[slot] != hour {
		w.hours[slot], w.slots[slot] = hour, 0
	}

	w.slots[slot] += n
	w.total += n
}

// Total is everything counted since the window was created.
func (w *Window) Total() int64 {
	return w.total
}

// Last24h is what was counted in the hour in progress and the 24 before it.
func (w *Window) Last24h(now time.Time) int64 {
	hour := hourOf(now)

	var sum int64
	for slot, held := range w.hours {
		if inWindow(held, hour) {
			sum += w.slots[slot]
		}
	}

	return sum
}

// sketchWindow is Window for distinct values: one sketch per hour, merged on
// read, plus one that is never cleared.
type sketchWindow struct {
	total Sketch
	slots [windowHours]Sketch
	hours [windowHours]int64
}

func (w *sketchWindow) add(hash uint64, now time.Time) {
	hour := hourOf(now)
	slot := slotOf(hour)

	if w.hours[slot] != hour {
		w.hours[slot] = hour
		w.slots[slot].Reset()
	}

	w.slots[slot].Add(hash)
	w.total.Add(hash)
}

func (w *sketchWindow) counts(now time.Time) (total, last24h int64) {
	hour := hourOf(now)

	var merged Sketch
	for slot, held := range w.hours {
		if inWindow(held, hour) {
			merged.Merge(&w.slots[slot])
		}
	}

	return w.total.Count(), merged.Count()
}

func hourOf(now time.Time) int64 {
	return now.Unix() / 3600
}

func slotOf(hour int64) int {
	return int(hour % windowHours)
}

// inWindow reports whether a slot's hour is recent enough to count. A slot that
// was never written holds hour zero and nothing, so it needs no special case.
func inWindow(held, current int64) bool {
	return held <= current && current-held < windowHours
}
