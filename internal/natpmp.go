package internal

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	natPMPVersion   = 0
	natPMPPort      = 5351
	opMapTCP        = 1
	opMapUDP        = 2
	resultSuccess   = 0
	defaultLifetime = 7200
	renewMargin     = 300
)

type natPMPClient struct {
	mu           sync.RWMutex
	enabled      bool
	gateway      string
	internalPort int
	externalPort int
	protocol     string
	lifetime     int
	mappedAt     time.Time
	cancel       chan struct{}
}

func newNATPMPClient() *natPMPClient {
	return &natPMPClient{
		cancel: make(chan struct{}),
	}
}

func (n *natPMPClient) start(gateway string, internalPort int, protocol string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.enabled {
		return nil
	}

	n.gateway = gateway
	n.internalPort = internalPort
	n.protocol = protocol
	n.lifetime = defaultLifetime

	extPort, err := n.sendMap(internalPort, protocol, defaultLifetime)
	if err != nil {
		return fmt.Errorf("nat-pmp map failed: %w", err)
	}

	n.externalPort = extPort
	n.mappedAt = time.Now()
	n.enabled = true

	go n.renewLoop()

	slog.Info("nat-pmp: port mapped",
		"internal", internalPort,
		"external", extPort,
		"protocol", protocol,
		"gateway", gateway,
		"lifetime", defaultLifetime,
	)
	return nil
}

func (n *natPMPClient) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.enabled {
		return
	}

	select {
	case <-n.cancel:
	default:
		close(n.cancel)
	}

	n.sendMap(n.internalPort, n.protocol, 0)

	n.enabled = false
	n.internalPort = 0
	n.externalPort = 0
	slog.Info("nat-pmp: port unmapped")
}

func (n *natPMPClient) status() (enabled bool, internalPort, externalPort int, protocol string, lifetime int, mappedAt time.Time, gateway string) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.enabled, n.internalPort, n.externalPort, n.protocol, n.lifetime, n.mappedAt, n.gateway
}

func (n *natPMPClient) renewLoop() {
	ticker := time.NewTicker(time.Duration(defaultLifetime-renewMargin) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.cancel:
			return
		case <-ticker.C:
			n.mu.RLock()
			port := n.internalPort
			proto := n.protocol
			n.mu.RUnlock()

			extPort, err := n.sendMap(port, proto, defaultLifetime)
			if err != nil {
				slog.Error("nat-pmp: renew failed", "error", err)
				continue
			}

			n.mu.Lock()
			n.externalPort = extPort
			n.mappedAt = time.Now()
			n.mu.Unlock()

			slog.Debug("nat-pmp: port renewed", "external", extPort)
		}
	}
}

func (n *natPMPClient) sendMap(internalPort int, protocol string, lifetime int) (int, error) {
	addr := net.JoinHostPort(n.gateway, fmt.Sprint(natPMPPort))
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return 0, fmt.Errorf("dial nat-pmp gateway %s: %w", addr, err)
	}
	defer conn.Close()

	op := opMapUDP
	if protocol == "tcp" {
		op = opMapTCP
	}

	req := make([]byte, 12)
	req[0] = natPMPVersion
	req[1] = byte(op)
	binary.BigEndian.PutUint16(req[4:6], uint16(internalPort))
	binary.BigEndian.PutUint16(req[6:8], uint16(internalPort))
	binary.BigEndian.PutUint32(req[8:12], uint32(lifetime))

	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return 0, fmt.Errorf("set deadline: %w", err)
	}

	if _, err := conn.Write(req); err != nil {
		return 0, fmt.Errorf("send nat-pmp request: %w", err)
	}

	resp := make([]byte, 16)
	nRead, err := conn.Read(resp)
	if err != nil {
		return 0, fmt.Errorf("read nat-pmp response: %w", err)
	}
	if nRead < 12 {
		return 0, fmt.Errorf("short nat-pmp response: %d bytes", nRead)
	}

	result := binary.BigEndian.Uint16(resp[2:4])
	if result != resultSuccess {
		return 0, fmt.Errorf("nat-pmp request failed: result code %d", result)
	}

	extPort := int(binary.BigEndian.Uint16(resp[6:8]))
	return extPort, nil
}
