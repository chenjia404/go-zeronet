package tracker

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenjia404/go-zeronet/internal/zeronet/conn"
	"github.com/chenjia404/go-zeronet/internal/zeronet/protocol"
)

const defaultLeft = 431102370

// Config 保存 tracker 子系统所需参数。
type Config struct {
	DataDir     string
	Trackers    []string
	DisableUDP  bool
	SharedLimit int
}

// Announcer 负责向 tracker 查询 peers，并维护共享 tracker 列表。
type Announcer struct {
	cfg Config

	mu           sync.Mutex
	peerID       []byte
	lastAnnounce map[string]time.Time
	shared       *sharedTrackerStore
	lastDiscover time.Time
}

// New 创建 tracker announcer。
func New(cfg Config) *Announcer {
	return &Announcer{
		cfg:          cfg,
		peerID:       newPeerID(),
		lastAnnounce: make(map[string]time.Time),
		shared:       newSharedTrackerStore(filepath.Join(cfg.DataDir, "trackers.json"), cfg.SharedLimit),
	}
}

// Trackers 返回当前可用 tracker 列表。
func (a *Announcer) Trackers() []string {
	return mergeTrackerLists(a.cfg.Trackers, a.shared.WorkingTrackers())
}

// Announce 查询某个站点的 peers。
func (a *Announcer) Announce(siteAddress string) ([]string, error) {
	a.mu.Lock()
	last := a.lastAnnounce[siteAddress]
	if time.Since(last) < 30*time.Second {
		a.mu.Unlock()
		return nil, nil
	}
	a.lastAnnounce[siteAddress] = time.Now()
	a.mu.Unlock()

	var discovered []string
	var lastErr error
	for _, trackerAddr := range a.Trackers() {
		peers, err := a.announceTracker(siteAddress, trackerAddr)
		if err != nil {
			a.shared.MarkError(trackerAddr)
			lastErr = err
			continue
		}
		a.shared.MarkSuccess(trackerAddr)
		discovered = append(discovered, peers...)
	}

	discovered = dedupeStrings(discovered)
	if len(discovered) == 0 {
		return nil, lastErr
	}
	return discovered, nil
}

// DiscoverTrackersFromPeers 从已连接 peer 获取共享 zero tracker。
func (a *Announcer) DiscoverTrackersFromPeers(peers []string) {
	a.mu.Lock()
	if time.Since(a.lastDiscover) < 5*time.Minute {
		a.mu.Unlock()
		return
	}
	a.lastDiscover = time.Now()
	a.mu.Unlock()

	if len(a.shared.WorkingTrackers()) > a.cfg.SharedLimit {
		return
	}

	for _, peer := range peers {
		client, err := conn.Dial(peer)
		if err != nil {
			continue
		}
		trackers, err := client.GetTrackers()
		_ = client.Close()
		if err != nil {
			continue
		}
		for _, trackerAddr := range trackers {
			a.shared.Add(trackerAddr)
		}
	}
}

func (a *Announcer) announceTracker(siteAddress, rawTracker string) ([]string, error) {
	trackerAddr, err := parseTrackerAddress(rawTracker)
	if err != nil {
		return nil, err
	}

	switch trackerAddr.Protocol {
	case "udp":
		if a.cfg.DisableUDP {
			return nil, fmt.Errorf("udp tracker 已禁用")
		}
		return a.announceUDP(siteAddress, trackerAddr)
	case "http", "https":
		return a.announceHTTP(siteAddress, trackerAddr)
	case "zero":
		return a.announceZero(siteAddress, trackerAddr)
	default:
		return nil, fmt.Errorf("未知 tracker 协议: %s", trackerAddr.Protocol)
	}
}

func (a *Announcer) announceUDP(siteAddress string, tracker trackerAddress) ([]string, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", tracker.HostPort())
	if err != nil {
		return nil, err
	}
	connUDP, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, err
	}
	defer connUDP.Close()
	_ = connUDP.SetDeadline(time.Now().Add(10 * time.Second))

	transactionID := mathrand.Uint32()
	connectReq := make([]byte, 16)
	binary.BigEndian.PutUint64(connectReq[0:], 0x41727101980)
	binary.BigEndian.PutUint32(connectReq[8:], 0)
	binary.BigEndian.PutUint32(connectReq[12:], transactionID)
	if _, err := connUDP.Write(connectReq); err != nil {
		return nil, err
	}

	connectResp := make([]byte, 2048)
	n, err := connUDP.Read(connectResp)
	if err != nil {
		return nil, err
	}
	if n < 16 || binary.BigEndian.Uint32(connectResp[0:4]) != 0 || binary.BigEndian.Uint32(connectResp[4:8]) != transactionID {
		return nil, fmt.Errorf("udp tracker connect 响应非法")
	}
	connectionID := binary.BigEndian.Uint64(connectResp[8:16])

	announceReq := make([]byte, 98)
	infoHash := sha1.Sum([]byte(siteAddress))
	copy(announceReq[0:8], uint64ToBytes(connectionID))
	binary.BigEndian.PutUint32(announceReq[8:12], 1)
	transactionID = mathrand.Uint32()
	binary.BigEndian.PutUint32(announceReq[12:16], transactionID)
	copy(announceReq[16:36], infoHash[:])
	copy(announceReq[36:56], a.peerID)
	binary.BigEndian.PutUint64(announceReq[56:64], 0)
	binary.BigEndian.PutUint64(announceReq[64:72], defaultLeft)
	binary.BigEndian.PutUint64(announceReq[72:80], 0)
	binary.BigEndian.PutUint32(announceReq[80:84], 2)
	binary.BigEndian.PutUint32(announceReq[84:88], 0)
	binary.BigEndian.PutUint32(announceReq[88:92], mathrand.Uint32())
	binary.BigEndian.PutUint32(announceReq[92:96], math.MaxUint32)
	binary.BigEndian.PutUint16(announceReq[96:98], 1)
	if _, err := connUDP.Write(announceReq); err != nil {
		return nil, err
	}

	n, err = connUDP.Read(connectResp)
	if err != nil {
		return nil, err
	}
	if n < 20 || binary.BigEndian.Uint32(connectResp[0:4]) != 1 || binary.BigEndian.Uint32(connectResp[4:8]) != transactionID {
		return nil, fmt.Errorf("udp tracker announce 响应非法")
	}
	return decodeCompactPeers(connectResp[20:n]), nil
}

func (a *Announcer) announceHTTP(siteAddress string, tracker trackerAddress) ([]string, error) {
	infoHash := sha1.Sum([]byte(siteAddress))
	params := url.Values{}
	params.Set("peer_id", string(a.peerID))
	params.Set("port", "1")
	params.Set("uploaded", "0")
	params.Set("downloaded", "0")
	params.Set("left", strconv.Itoa(defaultLeft))
	params.Set("compact", "1")
	params.Set("numwant", "30")
	params.Set("event", "started")

	requestURL := tracker.Raw + "?" + params.Encode() + "&info_hash=" + escapeQueryBytes(infoHash[:])
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "go-zeronet/0.1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	decoded, err := decodeBencode(body)
	if err != nil {
		return nil, err
	}
	dict, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("http tracker 响应格式非法")
	}
	peerData, ok := dict["peers"].([]byte)
	if !ok {
		return nil, fmt.Errorf("http tracker 未返回 compact peers")
	}
	return decodeCompactPeers(peerData), nil
}

func (a *Announcer) announceZero(siteAddress string, tracker trackerAddress) ([]string, error) {
	client, err := conn.Dial(tracker.HostPort())
	if err != nil {
		return nil, err
	}
	defer client.Close()

	addressHash := sha256.Sum256([]byte(siteAddress))
	res, err := client.Command("announce", protocol.Message{
		"hashes":     [][]byte{addressHash[:]},
		"onions":     []string{},
		"port":       1,
		"need_types": []string{"ip4", "ipv4", "ipv6"},
		"need_num":   30,
		"add":        []string{},
	})
	if err != nil {
		return nil, err
	}
	peersRaw, ok := res["peers"].([]any)
	if !ok || len(peersRaw) == 0 {
		return nil, fmt.Errorf("zero tracker 响应缺少 peers")
	}
	sitePeers, ok := peersRaw[0].(map[any]any)
	if !ok {
		return nil, fmt.Errorf("zero tracker peers 格式非法")
	}

	var peers []string
	for key, value := range sitePeers {
		keyStr, ok := key.(string)
		if !ok {
			continue
		}
		switch keyStr {
		case "ip4", "ipv4", "ipv6":
			for _, peer := range decodeAddressPeers(value) {
				peers = append(peers, fmt.Sprintf("%s:%d", peer.IP, peer.Port))
			}
		}
	}
	return dedupeStrings(peers), nil
}

type trackerAddress struct {
	Protocol string
	Host     string
	Port     int
	Raw      string
}

func (t trackerAddress) HostPort() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

func parseTrackerAddress(raw string) (trackerAddress, error) {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return trackerAddress{}, err
		}
		port := parsed.Port()
		if port == "" {
			if parsed.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			return trackerAddress{}, err
		}
		return trackerAddress{
			Protocol: parsed.Scheme,
			Host:     parsed.Hostname(),
			Port:     portNum,
			Raw:      raw,
		}, nil
	}

	protocolName, address, ok := strings.Cut(raw, "://")
	if !ok {
		return trackerAddress{}, fmt.Errorf("非法 tracker 地址: %s", raw)
	}
	host, port, err := splitLooseHostPort(address)
	if err != nil {
		return trackerAddress{}, err
	}
	if marker := strings.Index(host, "#"); marker >= 0 {
		host = host[:marker]
	}
	return trackerAddress{Protocol: protocolName, Host: host, Port: port, Raw: raw}, nil
}

func splitLooseHostPort(address string) (string, int, error) {
	index := strings.LastIndex(address, ":")
	if index < 0 {
		return "", 0, fmt.Errorf("tracker 缺少端口: %s", address)
	}
	host := address[:index]
	port, err := strconv.Atoi(address[index+1:])
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func decodeCompactPeers(raw []byte) []string {
	var peers []string
	for i := 0; i+5 < len(raw); i += 6 {
		ip := net.IP(raw[i : i+4]).String()
		port := binary.BigEndian.Uint16(raw[i+4 : i+6])
		if port == 0 {
			continue
		}
		peers = append(peers, fmt.Sprintf("%s:%d", ip, port))
	}
	return peers
}

func decodeAddressPeers(raw any) []conn.PeerAddress {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}

	var peers []conn.PeerAddress
	for _, item := range items {
		packed, ok := item.([]byte)
		if !ok {
			continue
		}
		ip, port, err := protocol.UnpackAddress(packed)
		if err != nil || port == 0 {
			continue
		}
		peers = append(peers, conn.PeerAddress{IP: ip, Port: port})
	}
	return peers
}

func newPeerID() []byte {
	peerID := make([]byte, 20)
	copy(peerID, []byte("-GZ0010-"))
	if _, err := rand.Read(peerID[8:]); err != nil {
		for i := 8; i < len(peerID); i++ {
			peerID[i] = byte(mathrand.Intn(256))
		}
	}
	return peerID
}

func uint64ToBytes(v uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, v)
	return buf
}

func escapeQueryBytes(raw []byte) string {
	var builder strings.Builder
	for _, b := range raw {
		builder.WriteString(fmt.Sprintf("%%%02X", b))
	}
	return builder.String()
}

func mergeTrackerLists(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var trackers []string
	for _, group := range groups {
		for _, tracker := range group {
			tracker = strings.TrimSpace(tracker)
			if tracker == "" {
				continue
			}
			if _, ok := seen[tracker]; ok {
				continue
			}
			seen[tracker] = struct{}{}
			trackers = append(trackers, tracker)
		}
	}
	sort.Strings(trackers)
	return trackers
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

type sharedTrackerStore struct {
	path        string
	sharedLimit int
	mu          sync.Mutex
	trackers    map[string]*sharedTrackerMeta
}

type sharedTrackerMeta struct {
	TimeSuccess int64 `json:"time_success"`
	NumError    int   `json:"num_error"`
}

func newSharedTrackerStore(path string, sharedLimit int) *sharedTrackerStore {
	store := &sharedTrackerStore{
		path:        path,
		sharedLimit: sharedLimit,
		trackers:    make(map[string]*sharedTrackerMeta),
	}
	store.load()
	return store
}

func (s *sharedTrackerStore) Add(tracker string) {
	if !strings.HasPrefix(tracker, "zero://") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.trackers[tracker]; ok {
		return
	}
	s.trackers[tracker] = &sharedTrackerMeta{}
	s.save()
}

func (s *sharedTrackerStore) MarkSuccess(tracker string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.trackers[tracker]; !ok {
		s.trackers[tracker] = &sharedTrackerMeta{}
	}
	s.trackers[tracker].TimeSuccess = time.Now().Unix()
	s.trackers[tracker].NumError = 0
	s.save()
}

func (s *sharedTrackerStore) MarkError(tracker string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.trackers[tracker]
	if !ok {
		return
	}
	meta.NumError++
	if meta.NumError > 30 && meta.TimeSuccess < time.Now().Add(-time.Hour).Unix() {
		delete(s.trackers, tracker)
	}
	s.save()
}

func (s *sharedTrackerStore) WorkingTrackers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var trackers []string
	for tracker, meta := range s.trackers {
		if meta.TimeSuccess > time.Now().Add(-time.Hour).Unix() {
			trackers = append(trackers, tracker)
		}
	}
	sort.Strings(trackers)
	if len(trackers) > s.sharedLimit && s.sharedLimit > 0 {
		return trackers[:s.sharedLimit]
	}
	return trackers
}

func (s *sharedTrackerStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file struct {
		Shared map[string]*sharedTrackerMeta `json:"shared"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return
	}
	if file.Shared != nil {
		s.trackers = file.Shared
	}
}

func (s *sharedTrackerStore) save() {
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	file := struct {
		Shared map[string]*sharedTrackerMeta `json:"shared"`
	}{Shared: s.trackers}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, raw, 0o644)
}
