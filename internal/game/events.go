package game

import (
	"encoding/binary"
	"errors"
	"math"
)

// Event is one answer in the log. Each is encoded as one byte
// (bits 0–5 symbol, bit 6 correct, bit 7 reserved) followed by Delta as a
// uvarint: 2 bytes for answers under two minutes apart, at most 4 within a day.
type Event struct {
	Symbol  Symbol
	Correct bool
	Delta   uint32 // seconds since the previous answer in the same log chunk
}

const (
	correctBit  = 1 << 6
	reservedBit = 1 << 7
)

func AppendEvent(dst []byte, e Event) []byte {
	b := byte(e.Symbol)
	if e.Correct {
		b |= correctBit
	}
	dst = append(dst, b)
	return binary.AppendUvarint(dst, uint64(e.Delta))
}

func DecodeEvents(b []byte) ([]Event, error) {
	var events []Event
	for len(b) > 0 {
		head := b[0]
		if head&reservedBit != 0 {
			return nil, errors.New("game: reserved event bit set")
		}
		sym := Symbol(head &^ correctBit)
		if sym >= NumSymbols {
			return nil, errors.New("game: bad event symbol")
		}
		delta, n := binary.Uvarint(b[1:])
		if n <= 0 || delta > math.MaxUint32 {
			return nil, errors.New("game: bad event delta")
		}
		events = append(events, Event{Symbol: sym, Correct: head&correctBit != 0, Delta: uint32(delta)})
		b = b[1+n:]
	}
	return events, nil
}

// Replay rebuilds a state from the full answer log. Current is left zero: the
// next pick is random and not part of the log.
func Replay(events []Event) State {
	s := initialState()
	for _, e := range events {
		s.apply(e.Symbol, e.Correct)
	}
	return s
}
