package internal

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	natPMPVersion = 0
	natPMPPort    = 5351

	opPublicAddr = 0
	opMapTCP     = 1
	opMapUDP     = 2

	resultSuccess = 0

	// Proton NAT-PMP: 60s lease, renew before expiry. Suggested public 1 / private 0
	// matches `natpmpc -a 1 0 udp 60` from Proton's manual setup docs.
	protonLifetime       = 60
	protonRenewEvery     = 45 * time.Second
	suggestedPublicPort  = 1
	natPMPMaxAttempts    = 5
	natPMPInitialBackoff = 200 * time.Millisecond
)

type natPMPClient struct {
	mu           sync.RWMutex
	enabled      bool
	gateway      string
	gatewayPort  int
	localIP      string
	internalPort int
	externalPort int
	protocol     string
	lifetime     int
	mappedAt     time.Time
	cancel       chan struct{}
	onPortChange func(port int)
}

func newNATPMPClient() *natPMPClient {
	return &natPMPClient{}
}

func (n *natPMPClient) setOnPortChange(fn func(int)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onPortChange = fn
}

func natPMPGateway(cfg parsedConfig) string {
	if len(cfg.Interface.DNS) > 0 && cfg.Interface.DNS[0] != "" {
		return cfg.Interface.DNS[0]
	}
	return "10.2.0.1"
}

func (n *natPMPClient) start(gateway, localIP string) error {
	n.mu.Lock()
	if n.enabled {
		n.mu.Unlock()
		return nil
	}
	if gateway == "" {
		n.mu.Unlock()
		return fmt.Errorf("nat-pmp gateway required")
	}
	n.gateway = gateway
	n.localIP = localIP
	n.lifetime = protonLifetime
	if n.gatewayPort == 0 {
		n.gatewayPort = natPMPPort
	}
	n.cancel = make(chan struct{})
	n.mu.Unlock()

	if _, err := n.probePublicAddress(); err != nil {
		n.resetInactive()
		return fmt.Errorf("nat-pmp probe: %w", err)
	}

	ext, err := n.mapUDPAndTCP(protonLifetime)
	if err != nil {
		n.resetInactive()
		return fmt.Errorf("nat-pmp map failed: %w", err)
	}

	n.mu.Lock()
	n.externalPort = ext
	n.internalPort = ext
	n.protocol = "udp+tcp"
	n.mappedAt = time.Now()
	n.enabled = true
	cb := n.onPortChange
	n.mu.Unlock()

	slog.Info("nat-pmp: port mapped",
		"internal", ext,
		"external", ext,
		"protocol", "udp+tcp",
		"gateway", gateway,
		"lifetime", protonLifetime,
	)
	if cb != nil {
		cb(ext)
	}
	go n.renewLoop()
	return nil
}

func (n *natPMPClient) resetInactive() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.gateway = ""
	n.localIP = ""
	n.enabled = false
}

func (n *natPMPClient) stop() {
	n.mu.Lock()
	if !n.enabled {
		n.mu.Unlock()
		return
	}
	if n.cancel != nil {
		select {
		case <-n.cancel:
		default:
			close(n.cancel)
		}
	}
	n.enabled = false
	n.internalPort = 0
	n.externalPort = 0
	n.mu.Unlock()

	if err := n.unmap(); err != nil {
		slog.Debug("nat-pmp: unmap", "error", err)
	}
	slog.Info("nat-pmp: port unmapped")
}

func (n *natPMPClient) unmap() error {
	_, err := n.mapUDPAndTCP(0)
	return err
}

func (n *natPMPClient) status() (enabled bool, internalPort, externalPort int, protocol string, lifetime int, mappedAt time.Time, gateway string) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.enabled, n.internalPort, n.externalPort, n.protocol, n.lifetime, n.mappedAt, n.gateway
}

func (n *natPMPClient) renewLoop() {
	ticker := time.NewTicker(protonRenewEvery)
	defer ticker.Stop()

	n.mu.RLock()
	cancel := n.cancel
	n.mu.RUnlock()
	if cancel == nil {
		return
	}

	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
			ext, err := n.mapUDPAndTCP(protonLifetime)
			if err != nil {
				slog.Error("nat-pmp: renew failed", "error", err)
				continue
			}

			n.mu.Lock()
			old := n.externalPort
			n.externalPort = ext
			n.internalPort = ext
			n.mappedAt = time.Now()
			cb := n.onPortChange
			n.mu.Unlock()

			if ext != old {
				slog.Info("nat-pmp: mapped port changed", "from", old, "to", ext)
				if cb != nil {
					cb(ext)
				}
			} else {
				slog.Debug("nat-pmp: port renewed", "external", ext)
			}
		}
	}
}

func (n *natPMPClient) probePublicAddress() (net.IP, error) {
	resp, err := n.exchange(encodePublicAddressRequest(), 12)
	if err != nil {
		return nil, err
	}
	return parsePublicAddressResponse(resp)
}

func (n *natPMPClient) mapUDPAndTCP(lifetime uint32) (int, error) {
	udpPort, err := n.mapProtocol(opMapUDP, lifetime)
	if err != nil {
		return 0, fmt.Errorf("udp: %w", err)
	}
	tcpPort, err := n.mapProtocol(opMapTCP, lifetime)
	if err != nil {
		return 0, fmt.Errorf("tcp: %w", err)
	}
	if lifetime > 0 && tcpPort != 0 && udpPort != 0 && tcpPort != udpPort {
		slog.Warn("nat-pmp: udp and tcp mapped ports differ", "udp", udpPort, "tcp", tcpPort)
	}
	if udpPort != 0 {
		return udpPort, nil
	}
	return tcpPort, nil
}

func (n *natPMPClient) mapProtocol(op byte, lifetime uint32) (int, error) {
	req := encodeMapRequest(op, suggestedPublicPort, 0, lifetime)
	resp, err := n.exchange(req, 16)
	if err != nil {
		return 0, err
	}
	_, ext, _, err := parseMapResponse(op, resp)
	if err != nil {
		return 0, err
	}
	return ext, nil
}

func (n *natPMPClient) exchange(req []byte, minResp int) ([]byte, error) {
	n.mu.RLock()
	gw := n.gateway
	gport := n.gatewayPort
	local := n.localIP
	n.mu.RUnlock()
	if gw == "" {
		return nil, fmt.Errorf("nat-pmp gateway required")
	}
	if gport == 0 {
		gport = natPMPPort
	}

	raddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(gw, strconv.Itoa(gport)))
	if err != nil {
		return nil, fmt.Errorf("resolve nat-pmp gateway: %w", err)
	}

	var laddr *net.UDPAddr
	if local != "" {
		ip := net.ParseIP(local)
		if ip == nil {
			return nil, fmt.Errorf("invalid nat-pmp local IP %s", local)
		}
		laddr = &net.UDPAddr{IP: ip.To4()}
	}

	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, fmt.Errorf("bind nat-pmp socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	backoff := natPMPInitialBackoff
	var lastErr error
	for i := 0; i < natPMPMaxAttempts; i++ {
		deadline := time.Now().Add(backoff + 100*time.Millisecond)
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("set deadline: %w", err)
		}
		if _, err := conn.WriteToUDP(req, raddr); err != nil {
			lastErr = fmt.Errorf("send nat-pmp request: %w", err)
		} else {
			buf := make([]byte, 16)
			nRead, _, err := conn.ReadFromUDP(buf)
			if err == nil && nRead >= minResp {
				return buf[:nRead], nil
			}
			if err != nil {
				lastErr = fmt.Errorf("read nat-pmp response: %w", err)
			} else {
				lastErr = fmt.Errorf("short nat-pmp response: %d bytes", nRead)
			}
		}
		if i == natPMPMaxAttempts-1 {
			break
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff *= 2
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nat-pmp no response from %s", raddr)
	}
	return nil, lastErr
}

func encodePublicAddressRequest() []byte {
	return []byte{natPMPVersion, opPublicAddr}
}

func encodeMapRequest(op byte, suggestedPublic, private uint16, lifetime uint32) []byte {
	req := make([]byte, 12)
	req[0] = natPMPVersion
	req[1] = op
	binary.BigEndian.PutUint16(req[4:6], private)
	binary.BigEndian.PutUint16(req[6:8], suggestedPublic)
	binary.BigEndian.PutUint32(req[8:12], lifetime)
	return req
}

func parsePublicAddressResponse(resp []byte) (net.IP, error) {
	if len(resp) < 12 {
		return nil, fmt.Errorf("short nat-pmp public address response: %d bytes", len(resp))
	}
	if resp[0] != natPMPVersion {
		return nil, fmt.Errorf("nat-pmp version %d", resp[0])
	}
	if resp[1] != 128+opPublicAddr {
		return nil, fmt.Errorf("nat-pmp unexpected opcode %d", resp[1])
	}
	result := binary.BigEndian.Uint16(resp[2:4])
	if result != resultSuccess {
		return nil, fmt.Errorf("nat-pmp public address failed: result code %d", result)
	}
	return net.IPv4(resp[8], resp[9], resp[10], resp[11]), nil
}

func parseMapResponse(wantOp byte, resp []byte) (internal, external, lifetime int, err error) {
	if len(resp) < 16 {
		return 0, 0, 0, fmt.Errorf("short nat-pmp mapping response: %d bytes", len(resp))
	}
	if resp[0] != natPMPVersion {
		return 0, 0, 0, fmt.Errorf("nat-pmp version %d", resp[0])
	}
	if resp[1] != 128+wantOp {
		return 0, 0, 0, fmt.Errorf("nat-pmp unexpected opcode %d want %d", resp[1], 128+wantOp)
	}
	result := binary.BigEndian.Uint16(resp[2:4])
	if result != resultSuccess {
		return 0, 0, 0, fmt.Errorf("nat-pmp request failed: result code %d", result)
	}
	internal = int(binary.BigEndian.Uint16(resp[8:10]))
	external = int(binary.BigEndian.Uint16(resp[10:12]))
	lifetime = int(binary.BigEndian.Uint32(resp[12:16]))
	return internal, external, lifetime, nil
}
