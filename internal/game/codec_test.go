package game

import (
	"bytes"
	"math"
	"testing"
)

// played returns a state after n random answers, plus the events that produced it.
func played(n int, seed uint64) (State, []Event) {
	rng := seeded(seed)
	s := NewState(rng)
	var events []Event
	for i := range n {
		correct := rng.IntN(3) != 0
		events = append(events, Event{Symbol: s.Current, Correct: correct, Delta: uint32(i % 300)})
		s = s.Answer(correct, rng)
	}
	return s, events
}

func TestStateRoundTrip(t *testing.T) {
	rng := seeded(11)
	s := NewState(rng)
	for range 5000 { // a whole game at 80% right, past every symbol joining
		s = s.Answer(rng.IntN(5) != 0, rng)
		blob := EncodeState(s)
		if len(blob) != StateSize || StateSize != 399 {
			t.Fatalf("encoded %d bytes, StateSize %d, want 399", len(blob), StateSize)
		}
		got, err := DecodeState(s.Seq, blob)
		if err != nil {
			t.Fatal(err)
		}
		if got != s {
			t.Fatalf("round trip mismatch at seq %d:\n got %+v\nwant %+v", s.Seq, got, s)
		}
	}
	if s.InPlay != NumSymbols {
		t.Errorf("only %d symbols in play", s.InPlay)
	}
}

func TestStateLayout(t *testing.T) {
	s := initialState()
	s.InPlay = 36
	s.Current = 35
	s.Symbols[0] = SymbolState{Score: 0x01020304, Recency: 7, Streak: -2, LastSeq: 0x0a0b0c0d}
	blob := EncodeState(s)
	want := []byte{4, 36, 35, 0x04, 0x03, 0x02, 0x01, 7, 0xfe, 0xff, 0x0d, 0x0c, 0x0b, 0x0a}
	if !bytes.Equal(blob[:len(want)], want) {
		t.Errorf("layout = % x, want % x", blob[:len(want)], want)
	}
	if got, err := DecodeState(s.Seq, blob); err != nil || got != s {
		t.Errorf("decode: err %v, equal %v", err, got == s)
	}
}

// Versions 1–3 stored a batch (level) instead of the number in play, and
// versions 2–3 a retired flag; they load with their levels' symbols in play.
func TestDecodeStateReadsOlderVersions(t *testing.T) {
	s, _ := played(200, 3)
	cases := []struct {
		version, batch byte
		retired        bool
		inPlay         uint8
	}{
		{1, 1, false, 9},
		{2, 2, true, 15},
		{3, 3, true, 23},
		{3, 4, false, 36},
		{3, 0x80 | 4, true, 36}, // completed
	}
	for _, c := range cases {
		blob := EncodeState(s)
		blob[0], blob[1] = c.version, c.batch
		if c.retired {
			blob[3+4] |= 0x80
		}
		want := s
		want.InPlay = c.inPlay
		if got, err := DecodeState(s.Seq, blob); err != nil || got != want {
			t.Errorf("version %d, batch byte %#x: err %v, in play %d, equal %v", c.version, c.batch, err, got.InPlay, got == want)
		}
	}
}

func TestDecodeStateRejectsBadInput(t *testing.T) {
	good := EncodeState(NewState(seeded(1)))
	mutate := func(changes ...[2]int) []byte {
		b := bytes.Clone(good)
		for _, c := range changes {
			b[c[0]] = byte(c[1])
		}
		return b
	}
	cases := map[string][]byte{
		"short":                     good[:StateSize-1],
		"long":                      append(bytes.Clone(good), 0),
		"unknown version":           mutate([2]int{0, 5}),
		"fewer than the start":      mutate([2]int{1, StartCount - 1}),
		"more than all":             mutate([2]int{1, NumSymbols + 1}),
		"current too high":          mutate([2]int{2, NumSymbols}),
		"recency above max":         mutate([2]int{3 + 4, MaxRecency + 1}),
		"reserved bits set":         mutate([2]int{3 + 4, 0x40 | 5}),
		"retired flag in version 4": mutate([2]int{3 + 4, 0x80 | 5}),
		"batch 0":                   mutate([2]int{0, 3}, [2]int{1, 0}),
		"batch too high":            mutate([2]int{0, 3}, [2]int{1, 5}),
		"batch flag bits":           mutate([2]int{0, 3}, [2]int{1, 0x40 | 1}),
		"retired flag in version 1": mutate([2]int{0, 1}, [2]int{1, 1}, [2]int{3 + 4, 0x80 | 5}),
		"completed in version 2":    mutate([2]int{0, 2}, [2]int{1, 0x80 | 4}),
	}
	for name, blob := range cases {
		if _, err := DecodeState(0, blob); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEventEncoding(t *testing.T) {
	cases := []struct {
		e    Event
		want []byte
	}{
		{Event{Symbol: 0, Correct: true, Delta: 0}, []byte{0x40, 0x00}},
		{Event{Symbol: 35, Correct: false, Delta: 127}, []byte{35, 0x7f}},
		{Event{Symbol: 1, Correct: false, Delta: 128}, []byte{1, 0x80, 0x01}},
		{Event{Symbol: 2, Correct: true, Delta: 86399}, []byte{0x42, 0xff, 0xa2, 0x05}},
	}
	for _, c := range cases {
		if got := AppendEvent(nil, c.e); !bytes.Equal(got, c.want) {
			t.Errorf("AppendEvent(%+v) = % x, want % x", c.e, got, c.want)
		}
	}
}

func TestEventsRoundTrip(t *testing.T) {
	_, events := played(500, 5)
	events = append(events, Event{Symbol: 9, Correct: true, Delta: math.MaxUint32})
	var buf []byte
	for _, e := range events {
		buf = AppendEvent(buf, e)
	}
	got, err := DecodeEvents(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("decoded %d events, want %d", len(got), len(events))
	}
	for i := range events {
		if got[i] != events[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], events[i])
		}
	}
}

func TestDecodeEventsRejectsBadInput(t *testing.T) {
	cases := map[string][]byte{
		"symbol out of range": {NumSymbols, 0},
		"reserved bit set":    {0x80, 0},
		"missing delta":       {0x40},
		"truncated varint":    {0x40, 0x80},
		"delta over 32 bits":  {0x40, 0xff, 0xff, 0xff, 0xff, 0x10},
	}
	for name, b := range cases {
		if _, err := DecodeEvents(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReplayRebuildsState(t *testing.T) {
	want, events := played(3000, 21)
	got := Replay(events)
	got.Current = want.Current // the next pick is random and not part of the log
	if got != want {
		t.Errorf("replay mismatch:\n got %+v\nwant %+v", got, want)
	}
}
