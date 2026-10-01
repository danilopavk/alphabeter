package game

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// State blob layout (little-endian), stored in kids.state:
//
//	version u8 | in_play u8 | current u8 | 36 × (score u32 | flags u8 | streak i16 | last_seq u32)
//
// flags: bits 0–3 recency, bits 4–7 reserved.
// Seq is not in the blob; it lives in its own column.
//
// Versions 1–3 come from the rules with levels. Instead of in_play they had a
// batch byte (bits 0–6 the number of unlocked levels, bit 7 completed in
// version 3), and versions 2–3 a retired flag (bit 7 of flags). They load with
// their unlocked levels' symbols in play; completed and retired are dropped.
const (
	stateVersion    = 4
	stateHeaderSize = 3
	symbolSize      = 11
	StateSize       = stateHeaderSize + NumSymbols*symbolSize

	batchMask    = 0x7f
	completedBit = 0x80
	recencyMask  = 0x0f
	retiredBit   = 0x80
)

// flagBits lists, per readable version, which bits the header's second byte
// and the symbol flag bytes may use.
var flagBits = map[byte]struct{ head, symbol byte }{
	1: {batchMask, recencyMask},
	2: {batchMask, recencyMask | retiredBit},
	3: {batchMask | completedBit, recencyMask | retiredBit},
	4: {0xff, recencyMask},
}

// levelInPlay is how many symbols were in play with 1–4 levels unlocked.
var levelInPlay = [...]uint8{9, 15, 23, 36}

func EncodeState(s State) []byte {
	b := make([]byte, stateHeaderSize, StateSize)
	b[0] = stateVersion
	b[1] = s.InPlay
	b[2] = byte(s.Current)
	for _, st := range s.Symbols {
		b = binary.LittleEndian.AppendUint32(b, st.Score)
		b = append(b, st.Recency)
		b = binary.LittleEndian.AppendUint16(b, uint16(st.Streak))
		b = binary.LittleEndian.AppendUint32(b, st.LastSeq)
	}
	return b
}

func DecodeState(seq uint32, b []byte) (State, error) {
	if len(b) != StateSize {
		return State{}, fmt.Errorf("game: state is %d bytes, want %d", len(b), StateSize)
	}
	allowed, ok := flagBits[b[0]]
	if !ok {
		return State{}, fmt.Errorf("game: unknown state version %d", b[0])
	}
	if b[1]&^allowed.head != 0 {
		return State{}, fmt.Errorf("game: bad header byte %#x", b[1])
	}
	s := State{Seq: seq, InPlay: b[1], Current: Symbol(b[2])}
	if b[0] < 4 {
		level := int(b[1] & batchMask)
		if level < 1 || level > len(levelInPlay) {
			return State{}, fmt.Errorf("game: bad level %d", level)
		}
		s.InPlay = levelInPlay[level-1]
	}
	if s.InPlay < StartCount || s.InPlay > NumSymbols {
		return State{}, fmt.Errorf("game: %d symbols in play", s.InPlay)
	}
	if s.Current >= NumSymbols {
		return State{}, errors.New("game: bad current symbol")
	}
	p := b[stateHeaderSize:]
	for i := range s.Symbols {
		flags := p[4]
		st := SymbolState{
			Score:   binary.LittleEndian.Uint32(p),
			Recency: flags & recencyMask,
			Streak:  int16(binary.LittleEndian.Uint16(p[5:])),
			LastSeq: binary.LittleEndian.Uint32(p[7:]),
		}
		if flags&^allowed.symbol != 0 || st.Recency > MaxRecency {
			return State{}, fmt.Errorf("game: bad flags %#x for %s", flags, Symbol(i))
		}
		s.Symbols[i] = st
		p = p[symbolSize:]
	}
	return s, nil
}
