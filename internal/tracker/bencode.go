package tracker

import (
	"bytes"
	"fmt"
	"strconv"
)

// bencodeDecoder 是 HTTP tracker 响应所需的最小 bencode 解码器。
type bencodeDecoder struct {
	raw []byte
	pos int
}

func decodeBencode(raw []byte) (any, error) {
	decoder := &bencodeDecoder{raw: raw}
	value, err := decoder.decode()
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (d *bencodeDecoder) decode() (any, error) {
	if d.pos >= len(d.raw) {
		return nil, fmt.Errorf("bencode 提前结束")
	}

	switch d.raw[d.pos] {
	case 'd':
		return d.decodeDict()
	case 'l':
		return d.decodeList()
	case 'i':
		return d.decodeInt()
	default:
		if d.raw[d.pos] >= '0' && d.raw[d.pos] <= '9' {
			return d.decodeBytes()
		}
		return nil, fmt.Errorf("未知 bencode 前缀: %q", d.raw[d.pos])
	}
}

func (d *bencodeDecoder) decodeDict() (map[string]any, error) {
	d.pos++
	back := make(map[string]any)
	for d.pos < len(d.raw) && d.raw[d.pos] != 'e' {
		keyRaw, err := d.decodeBytes()
		if err != nil {
			return nil, err
		}
		value, err := d.decode()
		if err != nil {
			return nil, err
		}
		back[string(keyRaw)] = value
	}
	if d.pos >= len(d.raw) {
		return nil, fmt.Errorf("bencode dict 缺少结束符")
	}
	d.pos++
	return back, nil
}

func (d *bencodeDecoder) decodeList() ([]any, error) {
	d.pos++
	var back []any
	for d.pos < len(d.raw) && d.raw[d.pos] != 'e' {
		value, err := d.decode()
		if err != nil {
			return nil, err
		}
		back = append(back, value)
	}
	if d.pos >= len(d.raw) {
		return nil, fmt.Errorf("bencode list 缺少结束符")
	}
	d.pos++
	return back, nil
}

func (d *bencodeDecoder) decodeInt() (int64, error) {
	d.pos++
	end := bytes.IndexByte(d.raw[d.pos:], 'e')
	if end < 0 {
		return 0, fmt.Errorf("bencode int 缺少结束符")
	}
	value, err := strconv.ParseInt(string(d.raw[d.pos:d.pos+end]), 10, 64)
	if err != nil {
		return 0, err
	}
	d.pos += end + 1
	return value, nil
}

func (d *bencodeDecoder) decodeBytes() ([]byte, error) {
	colon := bytes.IndexByte(d.raw[d.pos:], ':')
	if colon < 0 {
		return nil, fmt.Errorf("bencode bytes 缺少长度分隔符")
	}
	size, err := strconv.Atoi(string(d.raw[d.pos : d.pos+colon]))
	if err != nil {
		return nil, err
	}
	d.pos += colon + 1
	if d.pos+size > len(d.raw) {
		return nil, fmt.Errorf("bencode bytes 长度超出范围")
	}
	value := d.raw[d.pos : d.pos+size]
	d.pos += size
	return value, nil
}
