package protocol

// Message 是 ZeroNet v2 基于 msgpack 的基础消息格式。
type Message map[string]any

// Handshake 表示最小兼容握手字段集合。
type Handshake struct {
	Version        string   `msgpack:"version"`
	Protocol       string   `msgpack:"protocol"`
	UseBinType     bool     `msgpack:"use_bin_type"`
	PeerID         string   `msgpack:"peer_id"`
	FileServerPort int      `msgpack:"fileserver_port"`
	PortOpened     *bool    `msgpack:"port_opened,omitempty"`
	TargetIP       string   `msgpack:"target_ip"`
	Rev            int      `msgpack:"rev"`
	CryptSupported []string `msgpack:"crypt_supported"`
	Crypt          any      `msgpack:"crypt"`
	Time           int64    `msgpack:"time"`
}
