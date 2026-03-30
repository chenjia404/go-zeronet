package conn

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/chenjia404/go-zeronet/internal/zeronet/protocol"
)

const (
	readChunkSize = 512 * 1024
)

// Client 是当前阶段面向单个 Python ZeroNet peer 的最小兼容客户端。
type Client struct {
	addr   string
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer

	mu     sync.Mutex
	reqID  uint64
	closed bool
}

// FileResponse 保存 getFile 响应中的关键字段。
type FileResponse struct {
	Body     []byte
	Size     int64
	Location int64
}

// PeerAddress 表示从 PEX 获取到的 peer 地址。
type PeerAddress struct {
	IP   string
	Port int
}

// ModifiedFilesResponse 保存 listModified 结果。
type ModifiedFilesResponse map[string]int64

// Hashfield 保存 peer 宣告拥有的 optional 文件 hash id。
type Hashfield map[uint16]struct{}

// HashIDPeers 保存每个 hash id 对应的一组 peer 地址。
type HashIDPeers map[uint16][]PeerAddress

// Dial 建立 TCP 连接并完成 ZeroNet v2 握手。
func Dial(addr string) (*Client, error) {
	netConn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 peer %s 失败: %w", addr, err)
	}

	client := &Client{
		addr:   addr,
		conn:   netConn,
		reader: bufio.NewReader(netConn),
		writer: bufio.NewWriter(netConn),
	}

	if err := client.handshake(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// Close 关闭底层 TCP 连接。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

func (c *Client) handshake() error {
	handshake := protocol.Message{
		"cmd":    "handshake",
		"req_id": int64(0),
		"params": protocol.Message{
			"version":         "go-zeronet/0.1.0",
			"protocol":        "v2",
			"use_bin_type":    true,
			"peer_id":         "",
			"fileserver_port": 0,
			"target_ip":       "127.0.0.1",
			"rev":             1,
			"crypt_supported": []string{},
			"crypt":           nil,
			"time":            time.Now().Unix(),
		},
	}
	if err := c.writeMessage(handshake); err != nil {
		return err
	}

	msg, err := c.readMessage()
	if err != nil {
		return err
	}
	if cmd, _ := msg["cmd"].(string); cmd != "response" {
		return fmt.Errorf("握手响应非法: %+v", msg)
	}
	return nil
}

// Ping 向 peer 发送 ping，成功时返回 true。
func (c *Client) Ping() (bool, error) {
	msg, err := c.request("ping", protocol.Message{})
	if err != nil {
		return false, err
	}
	body, ok := msg["body"]
	if !ok {
		return false, nil
	}
	switch val := body.(type) {
	case []byte:
		return string(val) == "Pong!", nil
	case string:
		return val == "Pong!", nil
	default:
		return false, nil
	}
}

// GetFileChunk 请求一个文件分片。
func (c *Client) GetFileChunk(siteAddress, innerPath string, location int64, readBytes int64) (*FileResponse, error) {
	msg, err := c.request("getFile", protocol.Message{
		"site":       siteAddress,
		"inner_path": innerPath,
		"location":   location,
		"read_bytes": readBytes,
	})
	if err != nil {
		return nil, err
	}
	if errText, ok := msg["error"].(string); ok && errText != "" {
		return nil, fmt.Errorf("peer 返回错误: %s", errText)
	}

	resp := &FileResponse{}
	if body, ok := msg["body"].([]byte); ok {
		resp.Body = body
	}
	if body, ok := msg["body"].(string); ok {
		resp.Body = []byte(body)
	}
	resp.Size = toInt64(msg["size"])
	resp.Location = toInt64(msg["location"])
	return resp, nil
}

// GetFile 完整下载一个文件，内部通过多次 getFile 分块读取来兼容 Python 端协议。
func (c *Client) GetFile(siteAddress, innerPath string) ([]byte, error) {
	var out []byte
	var location int64
	var fileSize int64 = -1

	for {
		chunk, err := c.GetFileChunk(siteAddress, innerPath, location, readChunkSize)
		if err != nil {
			return nil, err
		}
		if fileSize < 0 {
			fileSize = chunk.Size
		}
		out = append(out, chunk.Body...)
		if chunk.Location >= chunk.Size {
			break
		}
		location = chunk.Location
	}

	if fileSize >= 0 && int64(len(out)) != fileSize {
		return nil, fmt.Errorf("文件长度不匹配: got=%d want=%d", len(out), fileSize)
	}
	return out, nil
}

// Pex 向 peer 请求更多站点 peer。
func (c *Client) Pex(siteAddress string, knownPeers []PeerAddress, needNum int) ([]PeerAddress, error) {
	request := protocol.Message{
		"site": siteAddress,
		"need": needNum,
	}

	if len(knownPeers) > 0 {
		var packedIPv4 [][]byte
		var packedIPv6 [][]byte
		for _, peer := range knownPeers {
			packed, err := protocol.PackAddress(peer.IP, peer.Port)
			if err != nil {
				continue
			}
			switch len(packed) {
			case 6:
				packedIPv4 = append(packedIPv4, packed)
			case 18:
				packedIPv6 = append(packedIPv6, packed)
			}
		}
		if len(packedIPv4) > 0 {
			request["peers"] = packedIPv4
		}
		if len(packedIPv6) > 0 {
			request["peers_ipv6"] = packedIPv6
		}
	}

	msg, err := c.request("pex", request)
	if err != nil {
		return nil, err
	}
	if errText, ok := msg["error"].(string); ok && errText != "" {
		return nil, fmt.Errorf("peer 返回 pex 错误: %s", errText)
	}

	var peers []PeerAddress
	peers = append(peers, decodePackedPeers(msg["peers"])...)
	peers = append(peers, decodePackedPeers(msg["peers_ipv6"])...)
	peers = append(peers, decodePackedOnionPeers(msg["peers_onion"])...)
	return peers, nil
}

// ListModified 查询站点中自给定时间后的 content.json 修改时间。
func (c *Client) ListModified(siteAddress string, since int64) (ModifiedFilesResponse, error) {
	msg, err := c.request("listModified", protocol.Message{
		"site":  siteAddress,
		"since": since,
	})
	if err != nil {
		return nil, err
	}
	if errText, ok := msg["error"].(string); ok && errText != "" {
		return nil, fmt.Errorf("peer 返回 listModified 错误: %s", errText)
	}

	modifiedFiles, ok := msg["modified_files"]
	if !ok {
		return nil, fmt.Errorf("listModified 响应缺少 modified_files")
	}
	return decodeModifiedFiles(modifiedFiles), nil
}

// GetHashfield 读取 peer 的 optional 文件 hashfield。
func (c *Client) GetHashfield(siteAddress string) (Hashfield, error) {
	msg, err := c.request("getHashfield", protocol.Message{
		"site": siteAddress,
	})
	if err != nil {
		return nil, err
	}
	if errText, ok := msg["error"].(string); ok && errText != "" {
		return nil, fmt.Errorf("peer 返回 getHashfield 错误: %s", errText)
	}

	raw, ok := msg["hashfield_raw"].([]byte)
	if !ok {
		return nil, fmt.Errorf("getHashfield 响应缺少 hashfield_raw")
	}
	return decodeHashfield(raw), nil
}

// GetTrackers 读取 peer 广播的共享 zero tracker 列表。
func (c *Client) GetTrackers() ([]string, error) {
	msg, err := c.request("getTrackers", protocol.Message{})
	if err != nil {
		return nil, err
	}
	if errText, ok := msg["error"].(string); ok && errText != "" {
		return nil, fmt.Errorf("peer 返回 getTrackers 错误: %s", errText)
	}
	items, ok := msg["trackers"].([]any)
	if !ok {
		return nil, fmt.Errorf("getTrackers 响应缺少 trackers")
	}
	var trackers []string
	for _, item := range items {
		switch val := item.(type) {
		case string:
			trackers = append(trackers, val)
		case []byte:
			trackers = append(trackers, string(val))
		}
	}
	return trackers, nil
}

// FindHashIDs 向 peer 查询一组 optional hash id 对应的 peer。
func (c *Client) FindHashIDs(siteAddress string, hashIDs []uint16) (HashIDPeers, error) {
	if len(hashIDs) == 0 {
		return HashIDPeers{}, nil
	}

	values := make([]int64, 0, len(hashIDs))
	for _, hashID := range hashIDs {
		values = append(values, int64(hashID))
	}

	msg, err := c.request("findHashIds", protocol.Message{
		"site":     siteAddress,
		"hash_ids": values,
	})
	if err != nil {
		return nil, err
	}
	if errText, ok := msg["error"].(string); ok && errText != "" {
		return nil, fmt.Errorf("peer 返回 findHashIds 错误: %s", errText)
	}

	back := make(HashIDPeers)
	mergeHashPeers(back, msg["peers"], false)
	mergeHashPeers(back, msg["peers_ipv6"], false)
	mergeHashPeers(back, msg["peers_onion"], true)
	return back, nil
}

// Command 发送任意 ZeroNet 命令并返回原始响应。
func (c *Client) Command(cmd string, params protocol.Message) (protocol.Message, error) {
	return c.request(cmd, params)
}

func (c *Client) request(cmd string, params protocol.Message) (protocol.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.reqID++
	msg := protocol.Message{
		"cmd":    cmd,
		"req_id": int64(c.reqID),
		"params": params,
	}
	if err := c.writeMessage(msg); err != nil {
		return nil, err
	}

	resp, err := c.readMessage()
	if err != nil {
		return nil, err
	}
	if toInt64(resp["to"]) != int64(c.reqID) {
		return nil, fmt.Errorf("响应 req_id 不匹配: resp=%d want=%d", toInt64(resp["to"]), c.reqID)
	}
	return resp, nil
}

func (c *Client) writeMessage(msg protocol.Message) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return fmt.Errorf("设置写超时失败: %w", err)
	}
	if err := protocol.WriteMessage(c.writer, msg); err != nil {
		return err
	}
	if err := c.writer.Flush(); err != nil {
		return fmt.Errorf("刷新写缓冲失败: %w", err)
	}
	return nil
}

func (c *Client) readMessage() (protocol.Message, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("设置读超时失败: %w", err)
	}
	msg, err := protocol.ReadMessage(c.reader)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func toInt64(v any) int64 {
	switch val := v.(type) {
	case int:
		return int64(val)
	case int8:
		return int64(val)
	case int16:
		return int64(val)
	case int32:
		return int64(val)
	case int64:
		return val
	case uint:
		return int64(val)
	case uint8:
		return int64(val)
	case uint16:
		return int64(val)
	case uint32:
		return int64(val)
	case uint64:
		return int64(val)
	case float32:
		return int64(val)
	case float64:
		return int64(val)
	default:
		return 0
	}
}

func decodePackedPeers(raw any) []PeerAddress {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}

	var peers []PeerAddress
	for _, item := range items {
		packed, ok := item.([]byte)
		if !ok {
			continue
		}
		ip, port, err := protocol.UnpackAddress(packed)
		if err != nil || port == 0 {
			continue
		}
		peers = append(peers, PeerAddress{IP: ip, Port: port})
	}
	return peers
}

func decodePackedOnionPeers(raw any) []PeerAddress {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}

	var peers []PeerAddress
	for _, item := range items {
		packed, ok := item.([]byte)
		if !ok {
			continue
		}
		ip, port, err := protocol.UnpackOnionAddress(packed)
		if err != nil || port == 0 {
			continue
		}
		peers = append(peers, PeerAddress{IP: ip, Port: port})
	}
	return peers
}

func decodeModifiedFiles(raw any) ModifiedFilesResponse {
	back := make(ModifiedFilesResponse)

	switch val := raw.(type) {
	case map[string]any:
		for key, item := range val {
			back[key] = toInt64(item)
		}
	case map[any]any:
		for key, item := range val {
			keyStr, ok := key.(string)
			if !ok {
				continue
			}
			back[keyStr] = toInt64(item)
		}
	}

	return back
}

func decodeHashfield(raw []byte) Hashfield {
	back := make(Hashfield)
	for i := 0; i+1 < len(raw); i += 2 {
		hashID := binary.LittleEndian.Uint16(raw[i : i+2])
		back[hashID] = struct{}{}
	}
	return back
}

func parseUint16(v string) (uint16, error) {
	value, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		return 0, err
	}
	return uint16(value), nil
}

func mergeHashPeers(target HashIDPeers, raw any, onion bool) {
	var items map[uint16][]PeerAddress
	switch val := raw.(type) {
	case map[any]any:
		items = decodeHashPeerMap(val, onion)
	case map[string]any:
		items = decodeStringHashPeerMap(val, onion)
	case map[uint16][]PeerAddress:
		items = val
	default:
		return
	}

	for hashID, peers := range items {
		target[hashID] = append(target[hashID], peers...)
	}
}

func decodeHashPeerMap(raw map[any]any, onion bool) map[uint16][]PeerAddress {
	back := make(map[uint16][]PeerAddress)
	for key, value := range raw {
		hashID := uint16(toInt64(key))
		if onion {
			back[hashID] = append(back[hashID], decodePackedOnionPeers(value)...)
		} else {
			back[hashID] = append(back[hashID], decodePackedPeers(value)...)
		}
	}
	return back
}

func decodeStringHashPeerMap(raw map[string]any, onion bool) map[uint16][]PeerAddress {
	back := make(map[uint16][]PeerAddress)
	for key, value := range raw {
		hashID, err := parseUint16(key)
		if err != nil {
			continue
		}
		if onion {
			back[hashID] = append(back[hashID], decodePackedOnionPeers(value)...)
		} else {
			back[hashID] = append(back[hashID], decodePackedPeers(value)...)
		}
	}
	return back
}
