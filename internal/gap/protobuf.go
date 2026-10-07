package gap

// The minimal protobuf wire-format reader.
//
// This is deliberately not github.com/google/pprof. Pulling that in for ~200
// lines of decoding would add a dependency to a package that currently has none
// and would sit awkwardly beside ten others across six shipped binaries. It is
// also kept in its own file because it is the one part of the decoder that knows
// nothing about profiles: it moves bytes and has no idea what a Sample is. That
// separation is what makes the hand-verified field numbers in pprof.go reviewable
// on their own.
//
// Only the four wire types a profile can contain are handled. Anything else is
// reported as unsupported rather than skipped, because silently skipping a
// length-delimited group would misalign every field after it and produce a
// profile full of plausible garbage.

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// readTag returns the field number and wire type, plus the remaining bytes.
//
// Field number 0 is rejected: it is invalid in the encoding, and a decoder that
// accepted it would loop forever on a message of nothing but zero tags.
func readTag(b []byte) (field uint64, wire uint64, rest []byte, err error) {
	v, rest, err := readVarint(b)
	if err != nil {
		return 0, 0, nil, err
	}
	field = v >> 3
	wire = v & 7
	if field == 0 {
		return 0, 0, nil, errors.New("protobuf field number 0 is invalid")
	}
	return field, wire, rest, nil
}

func readVarint(b []byte) (uint64, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errors.New("truncated protobuf varint")
	}
	return v, b[n:], nil
}

func readBytes(b []byte) ([]byte, []byte, error) {
	l, rest, err := readVarint(b)
	if err != nil {
		return nil, nil, err
	}
	// The length is compared against what is actually left rather than trusted:
	// a corrupt length is the single most common way a malformed profile turns
	// into a slice-out-of-range panic.
	if uint64(len(rest)) < l {
		return nil, nil, errors.New("truncated protobuf length-delimited field")
	}
	return rest[:l], rest[l:], nil
}

// skipValue advances past one value of the given wire type.
func skipValue(b []byte, wire uint64) ([]byte, error) {
	switch wire {
	case wireVarint:
		_, rest, err := readVarint(b)
		return rest, err
	case wireFixed64:
		if len(b) < 8 {
			return nil, errors.New("truncated fixed64")
		}
		return b[8:], nil
	case wireFixed32:
		if len(b) < 4 {
			return nil, errors.New("truncated fixed32")
		}
		return b[4:], nil
	case wireBytes:
		_, rest, err := readBytes(b)
		return rest, err
	default:
		return nil, fmt.Errorf("unsupported protobuf wire type %d", wire)
	}
}
