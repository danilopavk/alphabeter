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
	sawRetired, sawCompleted := false, false
	for range 5000 { // a whole game at 80% right, including free play
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
		for _, st := range s.Symbols {
			sawRetired = sawRetired || st.Retired
		}
		sawCompleted = sawCompleted || s.Completed
	}
	if !sawRetired || !sawCompleted {
		t.Errorf("flags not exercised: retired %v, completed %v", sawRetired, sawCompleted)
	}
}

func TestStateLayout(t *testing.T) {
	s := initialState()
	s.Batch = 4
	s.Completed = true
	s.Current = 35
	s.Symbols[0] = SymbolState{Score: 0x01020304, Recency: 7, Streak: -2, LastSeq: 0x0a0b0c0d, Retired: true}
	blob := EncodeState(s)
	want := []byte{3, 0x80 | 4, 35, 0x04, 0x03, 0x02, 0x01, 0x80 | 7, 0xfe, 0xff, 0x0d, 0x0c, 0x0b, 0x0a}
	if !bytes.Equal(blob[:len(want)], want) {
		t.Errorf("layout = % x, want % x", blob[:len(want)], want)
	}
	if got, err := DecodeState(s.Seq, blob); err != nil || got != s {
		t.Errorf("decode: err %v, equal %v", err, got == s)
	}
}

func TestDecodeStateReadsOlderVersions(t *testing.T) {
	s, _ := played(200, 3)
	for i := range s.Symbols {
		s.Symbols[i].Retired = false // version 1 had no retired flag
	}
	blob := EncodeState(s)
	for _, version := range []byte{1, 2} {
		blob[0] = version
		if got, err := DecodeState(s.Seq, blob); err != nil || got != s {
			t.Errorf("version %d blob: err %v, equal %v", version, err, got == s)
		}
	}

	v1 := bytes.Clone(blob)
	v1[0] = 1
	v1[3+4] |= 0x80
	if _, err := DecodeState(s.Seq, v1); err == nil {
		t.Error("version 1 blob with a retired flag accepted")
	}
	v2 := bytes.Clone(blob)
	v2[0] = 2
	v2[1] |= 0x80
	if _, err := DecodeState(s.Seq, v2); err == nil {
		t.Error("version 2 blob with a completed flag accepted")
	}
}

func TestDecodeStateRejectsBadInput(t *testing.T) {
	good := EncodeState(NewState(seeded(1)))
	mutate := func(i int, v byte) []byte {
		b := bytes.Clone(good)
		b[i] = v
		return b
	}
	cases := map[string][]byte{
		"short":             good[:StateSize-1],
		"long":              append(bytes.Clone(good), 0),
		"unknown version":   mutate(0, 4),
		"batch 0":           mutate(1, 0),
		"batch too high":    mutate(1, 5),
		"batch flag bits":   mutate(1, 0x40|1),
		"current too high":  mutate(2, NumSymbols),
		"recency above max": mutate(3+4, MaxRecency+1),
		"reserved bits set": mutate(3+4, 0x40|5),
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
