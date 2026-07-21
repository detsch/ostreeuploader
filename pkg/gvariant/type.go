package gvariant

import "fmt"

// kind enumerates the GVariant type classes this decoder understands.
type kind int

const (
	kindByte      kind = iota // y
	kindBool                  // b
	kindUint16                // q
	kindInt16                 // n
	kindUint32                // u
	kindInt32                 // i
	kindUint64                // t
	kindInt64                 // x
	kindDouble                // d
	kindString                // s
	kindObjPath               // o
	kindSignature             // g
	kindVariant               // v
	kindArray                 // a<elem>
	kindTuple                 // (...)
	kindDict                  // {kv} dict entry
)

// Type is a parsed GVariant type. fixed is the fixed serialized size (0 for
// variable-sized types); align is the alignment requirement in bytes.
type Type struct {
	kind  kind
	fixed uint64 // 0 => variable size
	align uint64

	elem  *Type   // array element / nil
	mtyps []*Type // tuple/dict member types

	// memberInfo is the precomputed per-member framing table for tuples/dicts,
	// mirroring GLib's GVariantMemberInfo. Built lazily by members().
	memberInfo []memberInfo
}

// ending classifies how a tuple member's end is located.
type ending int

const (
	endingFixed  ending = iota // member is fixed-size; end = start + fixed
	endingOffset               // end read from a framing offset
	endingLast                 // last variable member; end = size - offsets
)

// memberInfo holds the "magic constants" (a, b, c, i) used to locate a tuple
// member, per GLib gvarianttypeinfo.c. start = ((prev_end + a) & b) | c.
type memberInfo struct {
	typ    *Type
	a      uint64
	b      uint64
	c      uint64
	i      int // index of the framing offset to read for prev_end; -1 if none
	ending ending
	fixed  uint64
}

// parseType parses one complete type starting at s[i], returning the Type and
// the index just past it.
func parseType(s string, i int) (*Type, int, error) {
	if i >= len(s) {
		return nil, i, fmt.Errorf("%w: unexpected end of %q", ErrType, s)
	}
	switch s[i] {
	case 'y':
		return &Type{kind: kindByte, fixed: 1, align: 1}, i + 1, nil
	case 'b':
		return &Type{kind: kindBool, fixed: 1, align: 1}, i + 1, nil
	case 'q':
		return &Type{kind: kindUint16, fixed: 2, align: 2}, i + 1, nil
	case 'n':
		return &Type{kind: kindInt16, fixed: 2, align: 2}, i + 1, nil
	case 'u':
		return &Type{kind: kindUint32, fixed: 4, align: 4}, i + 1, nil
	case 'i':
		return &Type{kind: kindInt32, fixed: 4, align: 4}, i + 1, nil
	case 't':
		return &Type{kind: kindUint64, fixed: 8, align: 8}, i + 1, nil
	case 'x':
		return &Type{kind: kindInt64, fixed: 8, align: 8}, i + 1, nil
	case 'd':
		return &Type{kind: kindDouble, fixed: 8, align: 8}, i + 1, nil
	case 's':
		return &Type{kind: kindString, align: 1}, i + 1, nil
	case 'o':
		return &Type{kind: kindObjPath, align: 1}, i + 1, nil
	case 'g':
		return &Type{kind: kindSignature, align: 1}, i + 1, nil
	case 'v':
		return &Type{kind: kindVariant, align: 8}, i + 1, nil
	case 'a':
		elem, j, err := parseType(s, i+1)
		if err != nil {
			return nil, j, err
		}
		// An array is always variable-sized in serialized form; its alignment
		// equals the element's alignment.
		return &Type{kind: kindArray, elem: elem, align: elem.align}, j, nil
	case '(', '{':
		open := s[i]
		close := byte(')')
		k := kindTuple
		if open == '{' {
			close = '}'
			k = kindDict
		}
		var members []*Type
		j := i + 1
		for j < len(s) && s[j] != close {
			m, nj, err := parseType(s, j)
			if err != nil {
				return nil, nj, err
			}
			members = append(members, m)
			j = nj
		}
		if j >= len(s) || s[j] != close {
			return nil, j, fmt.Errorf("%w: unterminated tuple in %q", ErrType, s)
		}
		t := &Type{kind: k, mtyps: members}
		computeTupleLayout(t)
		return t, j + 1, nil
	default:
		return nil, i, fmt.Errorf("%w: unknown type char %q", ErrType, string(s[i]))
	}
}

// computeTupleLayout fills fixed size, alignment, and the member framing table
// for a tuple/dict, mirroring GLib's tuple_generate_table + tuple_set_base_info.
func computeTupleLayout(t *Type) {
	// Alignment is the max of member alignments.
	var align uint64 = 1
	for _, m := range t.mtyps {
		if m.align > align {
			align = m.align
		}
	}
	t.align = align

	// tuple_generate_table: maintain (a, b, c) such that to reach the current
	// position you add a, align to b (one-less form), then add c. i tracks the
	// framing-offset index of the last variable-sized member seen.
	info := make([]memberInfo, len(t.mtyps))
	var (
		i       = -1
		a, b, c uint64
	)
	for idx, m := range t.mtyps {
		d := m.align - 1 // alignment in one-less form
		e := m.fixed     // 0 for variable size

		// align to d (rules 1 & 2)
		if d <= b {
			c = alignDownOneLess(c, d)
		} else {
			a += alignDownOneLess(c, b)
			b = d
			c = 0
		}

		// tuple_table_append's a/b/c tweaks.
		na := a + (^b & c)
		nc := c & b
		mi := memberInfo{
			typ:   m,
			a:     na + b,
			b:     ^b,
			c:     nc,
			i:     i,
			fixed: m.fixed,
		}
		if m.fixed != 0 {
			mi.ending = endingFixed
		} else if idx == len(t.mtyps)-1 {
			mi.ending = endingLast
		} else {
			mi.ending = endingOffset
		}
		info[idx] = mi

		// move past the item
		if e == 0 {
			i++
			a, b, c = 0, 0, 0
		} else {
			c += e
		}
	}
	t.memberInfo = info

	// A tuple is fixed-size iff it contains no variable-sized members and is
	// non-empty; then fixed = its aligned total size. GLib treats the empty
	// tuple as fixed size 1.
	if len(t.mtyps) == 0 {
		t.fixed = 1
		return
	}
	fixed := true
	for _, m := range t.mtyps {
		if m.fixed == 0 {
			fixed = false
			break
		}
	}
	if fixed {
		// Total size = sum of members with alignment padding, rounded to align.
		var size uint64
		for _, m := range t.mtyps {
			size = alignUp(size, m.align)
			size += m.fixed
		}
		size = alignUp(size, t.align)
		t.fixed = size
	}
}

// alignDownOneLess aligns x up to (oneLess+1), where oneLess is an alignment in
// "one less than a power of two" form. This is GLib's tuple_align.
func alignDownOneLess(x, oneLess uint64) uint64 {
	return x + ((-x) & oneLess)
}

// members returns the precomputed framing table.
func (t *Type) members() []memberInfo { return t.memberInfo }

// String renders the type signature.
func (t *Type) String() string {
	switch t.kind {
	case kindByte:
		return "y"
	case kindBool:
		return "b"
	case kindUint16:
		return "q"
	case kindInt16:
		return "n"
	case kindUint32:
		return "u"
	case kindInt32:
		return "i"
	case kindUint64:
		return "t"
	case kindInt64:
		return "x"
	case kindDouble:
		return "d"
	case kindString:
		return "s"
	case kindObjPath:
		return "o"
	case kindSignature:
		return "g"
	case kindVariant:
		return "v"
	case kindArray:
		return "a" + t.elem.String()
	case kindTuple, kindDict:
		open, close := "(", ")"
		if t.kind == kindDict {
			open, close = "{", "}"
		}
		s := open
		for _, m := range t.mtyps {
			s += m.String()
		}
		return s + close
	}
	return "?"
}
