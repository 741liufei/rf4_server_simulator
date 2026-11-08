package proto

import "fmt"

// Field 是对象的一个位置化字段（marker 0x01 内部）。
//
// 编码时按类的 ops 序列逐位对应（Op 仅信息性，真正决定线型的是 ops 表位置），
// U16STR/U32N 解码后都归为 Str，重编码由 ops 决定前缀宽度。
type Field struct {
	Op    string  // 信息性：U32 U64 BYTE STR U16 U16STR U32N VAL BYTES GUID16 OPTGUID OPT FLOAT DOUBLE DICT ARGLIST ARRAY U32ARR STR3 KV
	Num   *uint64 // U32/U64/BYTE/U16/OPT（U16 null → nil）
	Str   *string // STR/U16STR/U32N（null → nil）
	Hex   string  // GUID16/OPTGUID/BYTES（OPTGUID null → ""）
	Val   *Value  // VAL/DICT/ARGLIST/ARRAY/U32ARR/STR3/KV
	Float float64 // FLOAT/DOUBLE（FLOAT 编码时转 float32）
}

type reader struct {
	data []byte
	pos  int
}

func (r *reader) remaining() int { return len(r.data) - r.pos }

func (r *reader) need(n int, what string) error {
	if r.pos+n > len(r.data) {
		return errAt("decode", what, "EOF", r.pos)
	}
	return nil
}

func (r *reader) u8(what string) (uint8, error) {
	if err := r.need(1, what); err != nil {
		return 0, err
	}
	v := r.data[r.pos]
	r.pos++
	return v, nil
}

func (r *reader) u16(what string) (uint16, error) {
	if err := r.need(2, what); err != nil {
		return 0, err
	}
	v := uint16(r.data[r.pos]) | uint16(r.data[r.pos+1])<<8
	r.pos += 2
	return v, nil
}

func (r *reader) u32(what string) (uint32, error) {
	if err := r.need(4, what); err != nil {
		return 0, err
	}
	v := uint32(r.data[r.pos]) | uint32(r.data[r.pos+1])<<8 |
		uint32(r.data[r.pos+2])<<16 | uint32(r.data[r.pos+3])<<24
	r.pos += 4
	return v, nil
}

func (r *reader) u64(what string) (uint64, error) {
	if err := r.need(8, what); err != nil {
		return 0, err
	}
	var v uint64
	for k := 7; k >= 0; k-- {
		v = v<<8 | uint64(r.data[r.pos+k])
	}
	r.pos += 8
	return v, nil
}

func (r *reader) bytes(n int, what string) ([]byte, error) {
	if err := r.need(n, what); err != nil {
		return nil, err
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// shortStr 读 byte len + bytes；0xFF → nil。
func (r *reader) shortStr(what string) (*string, error) {
	ln, err := r.u8(what)
	if err != nil {
		return nil, err
	}
	if ln == byte(StrNullShort) {
		return nil, nil
	}
	raw, err := r.bytes(int(ln), what)
	if err != nil {
		return nil, err
	}
	s := string(raw) // 协议串为 ASCII；非法 UTF-8 时原样保留字节
	return &s, nil
}

// DecodeValue 从 data[pos] 开始解码一个 marker 值，返回树节点和新位置。
func DecodeValue(data []byte, pos int, table *OpsTable) (*Value, int, error) {
	return decodeValue(&reader{data: data, pos: pos}, table, 0)
}

func decodeValue(r *reader, table *OpsTable, depth int) (*Value, int, error) {
	if depth > MaxDepth {
		return nil, r.pos, errAt("decode", "value", "depth too deep", r.pos)
	}
	start := r.pos
	m, err := r.u8("marker")
	if err != nil {
		return nil, r.pos, err
	}
	switch m {
	case 0x00:
		return &Value{Kind: KindNull}, r.pos, nil

	case 0x01:
		tid, err := r.u32("obj type_id")
		if err != nil {
			return nil, r.pos, err
		}
		if tid == TypeSentinelValList {
			var items []*Value
			for r.remaining() > 0 {
				v, _, err := decodeValue(r, table, depth+1)
				if err != nil {
					return nil, r.pos, err
				}
				items = append(items, v)
			}
			return &Value{Kind: KindValList, Items: items}, r.pos, nil
		}
		cls := table.ClassForType(int32(tid))
		ops := table.OpsFor(cls)
		fields, err := decodeFields(r, ops, table, depth+1)
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindObj, TypeID: tid, Class: cls, Fields: fields}, r.pos, nil

	case 0x02:
		tname, err := r.shortStr("dict type")
		if err != nil {
			return nil, r.pos, err
		}
		cnt, err := r.u8("dict count")
		if err != nil {
			return nil, r.pos, err
		}
		pairs := make([]Pair, 0, cnt)
		for k := 0; k < int(cnt); k++ {
			key, err := r.shortStr("dict key")
			if err != nil {
				return nil, r.pos, err
			}
			v, _, err := decodeValue(r, table, depth+1)
			if err != nil {
				return nil, r.pos, err
			}
			pairs = append(pairs, Pair{K: key, V: v})
		}
		tn := ""
		if tname != nil {
			tn = *tname
		}
		return &Value{Kind: KindDict, TypeName: tn, Pairs: pairs}, r.pos, nil

	case 0x03:
		code, err := r.shortStr("args code")
		if err != nil {
			return nil, r.pos, err
		}
		cnt, err := r.u16("args count")
		if err != nil {
			return nil, r.pos, err
		}
		items := make([]*Value, 0, cnt)
		for k := 0; k < int(cnt); k++ {
			v, _, err := decodeValue(r, table, depth+1)
			if err != nil {
				return nil, r.pos, err
			}
			items = append(items, v)
		}
		return &Value{Kind: KindArgs, Code: code, Items: items}, r.pos, nil

	case 0x04:
		cnt, err := r.u16("array count")
		if err != nil {
			return nil, r.pos, err
		}
		items := make([]*Value, 0, cnt)
		for k := 0; k < int(cnt); k++ {
			ln, err := r.u32("array elem len")
			if err != nil {
				return nil, r.pos, err
			}
			if ln == BytesNull {
				items = append(items, &Value{Kind: KindBytes, IsNull: true})
				continue
			}
			raw, err := r.bytes(int(ln), "array elem")
			if err != nil {
				return nil, r.pos, err
			}
			items = append(items, &Value{Kind: KindBytes, Hex: hexEncode(raw)})
		}
		return &Value{Kind: KindArray, Items: items}, r.pos, nil

	case 0x05:
		cnt, err := r.u16("u32arr count")
		if err != nil {
			return nil, r.pos, err
		}
		items := make([]*Value, 0, cnt)
		for k := 0; k < int(cnt); k++ {
			v, err := r.u32("u32arr elem")
			if err != nil {
				return nil, r.pos, err
			}
			items = append(items, &Value{Kind: KindU32, Num: uint64(v)})
		}
		return &Value{Kind: KindU32Arr, Items: items}, r.pos, nil

	case 0x06:
		cnt, err := r.u16("guidarr count")
		if err != nil {
			return nil, r.pos, err
		}
		items := make([]*Value, 0, cnt)
		for k := 0; k < int(cnt); k++ {
			raw, err := r.bytes(16, "guidarr elem")
			if err != nil {
				return nil, r.pos, err
			}
			items = append(items, &Value{Kind: KindGuid, Hex: hexEncode(raw)})
		}
		return &Value{Kind: KindGuidArr, Items: items}, r.pos, nil

	case 0x07:
		tname, err := r.shortStr("typed_u32arr type")
		if err != nil {
			return nil, r.pos, err
		}
		cnt, err := r.u16("typed_u32arr count")
		if err != nil {
			return nil, r.pos, err
		}
		items := make([]*Value, 0, cnt)
		for k := 0; k < int(cnt); k++ {
			v, err := r.u32("typed_u32arr elem")
			if err != nil {
				return nil, r.pos, err
			}
			items = append(items, &Value{Kind: KindU32, Num: uint64(v)})
		}
		tn := ""
		if tname != nil {
			tn = *tname
		}
		return &Value{Kind: KindTypedU32Arr, TypeName: tn, Items: items}, r.pos, nil

	case 0x08:
		ln, err := r.u32("bytes len")
		if err != nil {
			return nil, r.pos, err
		}
		raw, err := r.bytes(int(ln), "bytes")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindBytes, Hex: hexEncode(raw)}, r.pos, nil

	case 0x09:
		s1, err := r.shortStr("str3[0]")
		if err != nil {
			return nil, r.pos, err
		}
		s2, err := r.shortStr("str3[1]")
		if err != nil {
			return nil, r.pos, err
		}
		s3, err := r.shortStr("str3[2]")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindStr3, S3: [3]*string{s1, s2, s3}}, r.pos, nil

	case 0x0A:
		s1, err := r.shortStr("kv key")
		if err != nil {
			return nil, r.pos, err
		}
		s2, err := r.shortStr("kv value")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindKV, S3: [3]*string{s1, s2}}, r.pos, nil

	case 0x0B:
		v, err := r.u64("datetime")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindDateTime, Num: v}, r.pos, nil

	case 0x0C, 0x17:
		// 0x17 是 null guid 短格式，后随 0 字节载荷。
		// 解码后统一表示为零值 GUID，并保留 marker 以便原样编码。
		if m == 0x17 {
			return &Value{Kind: KindGuid, Marker: m, Hex: "00000000000000000000000000000000"}, r.pos, nil
		}
		raw, err := r.bytes(16, "guid")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindGuid, Marker: m, Hex: hexEncode(raw)}, r.pos, nil

	case 0x0D, 0x10, 0x13:
		v, err := r.u32("u32")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindU32, Marker: m, Num: uint64(v)}, r.pos, nil

	case 0x0E:
		return &Value{Kind: KindBool, Bool: true}, r.pos, nil
	case 0x0F:
		return &Value{Kind: KindBool, Bool: false}, r.pos, nil

	case 0x11, 0x12:
		v, err := r.u64("u64")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindU64, Marker: m, Num: v}, r.pos, nil

	case 0x14:
		s, err := r.shortStr("str")
		if err != nil {
			return nil, r.pos, err
		}
		return &Value{Kind: KindStr, Marker: m, Str: s}, r.pos, nil

	case 0x15:
		ln, err := r.u16("str u16 len")
		if err != nil {
			return nil, r.pos, err
		}
		if uint32(ln) == StrNullU16 {
			return &Value{Kind: KindStr, Marker: m}, r.pos, nil
		}
		raw, err := r.bytes(int(ln), "str u16")
		if err != nil {
			return nil, r.pos, err
		}
		s := string(raw)
		return &Value{Kind: KindStr, Marker: m, Str: &s}, r.pos, nil

	case 0x16:
		ln, err := r.u32("str u32 len")
		if err != nil {
			return nil, r.pos, err
		}
		if ln == StrNullU32 {
			return &Value{Kind: KindStr, Marker: m}, r.pos, nil
		}
		raw, err := r.bytes(int(ln), "str u32")
		if err != nil {
			return nil, r.pos, err
		}
		s := string(raw)
		return &Value{Kind: KindStr, Marker: m, Str: &s}, r.pos, nil

	case 0xFF:
		// 探测性保留；标准 args 元素空值应使用 0x00。
		return &Value{Kind: KindElemNull}, r.pos, nil
	}
	_ = start
	return nil, r.pos, errAt("decode", "marker", fmt.Sprintf("unknown marker 0x%02x", m), r.pos)
}

// decodeFields 按位置化 op 序列解码对象字段。
func decodeFields(r *reader, ops []string, table *OpsTable, depth int) ([]Field, error) {
	if ops == nil {
		return nil, nil
	}
	fields := make([]Field, 0, len(ops))
	for _, op := range ops {
		f := Field{Op: op}
		switch op {
		case "U32":
			v, err := r.u32(op)
			if err != nil {
				return nil, err
			}
			f.Num = ptrU64(uint64(v))
		case "U64":
			v, err := r.u64(op)
			if err != nil {
				return nil, err
			}
			f.Num = ptrU64(v)
		case "BYTE":
			v, err := r.u8(op)
			if err != nil {
				return nil, err
			}
			f.Num = ptrU64(uint64(v))
		case "STR":
			s, err := r.shortStr(op)
			if err != nil {
				return nil, err
			}
			f.Str = s
		case "U16":
			v, err := r.u16(op)
			if err != nil {
				return nil, err
			}
			if uint32(v) == StrNullU16 {
				f.Num = nil
			} else {
				f.Num = ptrU64(uint64(v))
			}
		case "U16STR":
			ln, err := r.u16(op)
			if err != nil {
				return nil, err
			}
			if uint32(ln) == StrNullU16 {
				f.Str = nil
			} else {
				raw, err := r.bytes(int(ln), op)
				if err != nil {
					return nil, err
				}
				s := string(raw)
				f.Str = &s
			}
		case "U32N":
			ln, err := r.u32(op)
			if err != nil {
				return nil, err
			}
			if ln == StrNullU32 {
				f.Str = nil
			} else {
				raw, err := r.bytes(int(ln), op)
				if err != nil {
					return nil, err
				}
				s := string(raw)
				f.Str = &s
			}
		case "VAL":
			v, _, err := decodeValue(r, table, depth+1)
			if err != nil {
				return nil, err
			}
			f.Val = v
		case "BYTES":
			ln, err := r.u32(op)
			if err != nil {
				return nil, err
			}
			raw, err := r.bytes(int(ln), op)
			if err != nil {
				return nil, err
			}
			f.Hex = hexEncode(raw)
		case "GUID16":
			raw, err := r.bytes(16, op)
			if err != nil {
				return nil, err
			}
			f.Hex = hexEncode(raw)
		case "OPTGUID":
			flag, err := r.u8(op)
			if err != nil {
				return nil, err
			}
			if flag != 0 {
				raw, err := r.bytes(16, op)
				if err != nil {
					return nil, err
				}
				f.Hex = hexEncode(raw)
			}
		case "OPT":
			flag, err := r.u8(op)
			if err != nil {
				return nil, err
			}
			f.Num = ptrU64(uint64(flag))
		case "FLOAT":
			raw, err := r.bytes(4, op)
			if err != nil {
				return nil, err
			}
			f.Float = float64(F32FromBits(uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24))
		case "DOUBLE":
			raw, err := r.bytes(8, op)
			if err != nil {
				return nil, err
			}
			var bits uint64
			for k := 7; k >= 0; k-- {
				bits = bits<<8 | uint64(raw[k])
			}
			f.Float = F64FromBits(bits)
		case "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV":
			v, _, err := decodeValue(r, table, depth+1)
			if err != nil {
				return nil, err
			}
			f.Val = v
		default:
			if len(op) > 5 && op[:5] == "CALL:" {
				return nil, errAt("decode", "op", "unhandled field op "+op, r.pos)
			}
			return nil, errAt("decode", "op", "unknown field op "+op, r.pos)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

func ptrU64(v uint64) *uint64 { return &v }

// hexEncode 小写 hex。
func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0F]
	}
	return string(out)
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errAt("hex", "decode", "odd hex length", 0)
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, errAt("hex", "decode", "bad hex", i*2)
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
