package protocol

import (
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// PackAddress 将 ip:port 打包为 ZeroNet 使用的 6/18 字节格式。
func PackAddress(ip string, port int) ([]byte, error) {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return nil, fmt.Errorf("无效 IP: %s", ip)
	}

	if ipv4 := parsedIP.To4(); ipv4 != nil {
		packed := make([]byte, 6)
		copy(packed[:4], ipv4)
		binary.LittleEndian.PutUint16(packed[4:], uint16(port))
		return packed, nil
	}

	ipv6 := parsedIP.To16()
	if ipv6 == nil {
		return nil, fmt.Errorf("无效 IPv6: %s", ip)
	}
	packed := make([]byte, 18)
	copy(packed[:16], ipv6)
	binary.LittleEndian.PutUint16(packed[16:], uint16(port))
	return packed, nil
}

// UnpackAddress 将 ZeroNet 的 packed 地址解码为 ip:port。
func UnpackAddress(packed []byte) (string, int, error) {
	switch len(packed) {
	case 6:
		ip := net.IP(packed[:4]).String()
		port := int(binary.LittleEndian.Uint16(packed[4:]))
		return ip, port, nil
	case 18:
		ip := net.IP(packed[:16]).String()
		port := int(binary.LittleEndian.Uint16(packed[16:]))
		return ip, port, nil
	default:
		return "", 0, fmt.Errorf("无效 packed address 长度: %d", len(packed))
	}
}

// UnpackOnionAddress 解码 onion packed 地址。
func UnpackOnionAddress(packed []byte) (string, int, error) {
	if len(packed) < 3 {
		return "", 0, fmt.Errorf("无效 onion packed address 长度: %d", len(packed))
	}
	encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	onion := strings.ToLower(encoder.EncodeToString(packed[:len(packed)-2])) + ".onion"
	port := int(binary.LittleEndian.Uint16(packed[len(packed)-2:]))
	return onion, port, nil
}
