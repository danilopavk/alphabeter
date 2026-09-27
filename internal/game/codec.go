package game

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// State blob layout (little-endian), stored in kids.state:
//
//	version u8 | batch u8 | current u8 | 36 × (score u32 | flags u8 | streak i16 | last_seq u32)
//
// batch: bits 0–6 batch number, bit 7 completed (version 3+).
// flags: bits 0–3 recency, bit 7 retired (version 2+), bits 4–6 reserved.
// Seq is not in the blob; it lives in its own column.
const (
	stateVersion    = 3
	stateHeaderSize = 3
	symbolSize      = 11
	StateSize       = stateHeaderSize + NumSymbols*symbolSize

	batchMask    = 0x7f
	completedBit = 0x80
	recencyMask  = 0x0f
	retiredBit   = 0x80
)

// flagBits lists, per readable version, which bits the batch and symbol
// flag bytes may use.
var flagBits = map[byte]struct{ batch, symbol byte }{
	1: {batchMask, recencyMask},
	2: {batchMask, recencyMask | retiredBit},
	3: {batchMask | completedBit, recencyMask | retiredBit},
}

func EncodeState(s State) []byte {
	b := make([]byte, stateHeaderSize, StateSize)
	b[0] = stateVersion
	b[1] = s.Batch
	if s.Completed {
		b[1] |= completedBit
	}
	b[2] = byte(s.Current)
	for _, st := range s.Symbols {
		b = binary.LittleEndian.AppendUint32(b, st.Score)
		flags := st.Recency
		if st.Retired {
			flags |= retiredBit
		}
		b = append(b, flags)
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
	if b[1]&^allowed.batch != 0 {
		return State{}, fmt.Errorf("game: bad batch byte %#x", b[1])
	}
	s := State{Seq: seq, Batch: b[1] & batchMask, Completed: b[1]&completedBit != 0, Current: Symbol(b[2])}
	if s.Batch < 1 || int(s.Batch) > len(Batches) {
		return State{}, fmt.Errorf("game: bad batch %d", s.Batch)
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
			Retired: flags&retiredBit != 0,
		}
		if flags&^allowed.symbol != 0 || st.Recency > MaxRecency {
			return State{}, fmt.Errorf("game: bad flags %#x for %s", flags, Symbol(i))
		}
		s.Symbols[i] = st
		p = p[symbolSize:]
	}
	return s, nil
}
