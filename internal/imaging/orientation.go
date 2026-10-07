// Package imaging chuẩn bị ảnh egress pure Go: chỉ đọc 1 tag EXIF Orientation,
// downscale và re-encode JPEG (re-encode tự bỏ EXIF/GPS).
package imaging

import "encoding/binary"

// ReadOrientation đọc tag EXIF Orientation (0x0112) từ APP1 của JPEG.
// Trả 1..8; không có EXIF / không phải JPEG / dữ liệu hỏng → 1.
func ReadOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+3 < len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		switch {
		case marker == 0xDA || marker == 0xD9: // SOS/EOI — hết metadata
			return 1
		case marker >= 0xD0 && marker <= 0xD8: // RST/SOI — không có length
			i += 2
		default:
			segLen := int(data[i+2])<<8 | int(data[i+3])
			if segLen < 2 || i+2+segLen > len(data) {
				return 1
			}
			if marker == 0xE1 {
				payload := data[i+4 : i+2+segLen]
				if len(payload) >= 8 && string(payload[:6]) == "Exif\x00\x00" {
					return orientationFromTIFF(payload[6:])
				}
			}
			i += 2 + segLen
		}
	}
	return 1
}

// orientationFromTIFF tìm tag 0x0112 trong IFD0 của khối TIFF EXIF.
func orientationFromTIFF(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch {
	case t[0] == 'I' && t[1] == 'I':
		bo = binary.LittleEndian
	case t[0] == 'M' && t[1] == 'M':
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	p := off + 2
	for i := 0; i < n; i++ {
		if p+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[p:p+2]) == 0x0112 && bo.Uint16(t[p+2:p+4]) == 3 {
			v := int(bo.Uint16(t[p+8 : p+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
		p += 12
	}
	return 1
}
