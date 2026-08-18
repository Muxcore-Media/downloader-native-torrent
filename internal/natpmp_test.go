package internal

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/anacrolix/torrent"
)

func TestParseMapResponseRFCOffsets(t *testing.T) {
	resp := make([]byte, 16)
	resp[0] = natPMPVersion
	resp[1] = 128 + opMapUDP
	binary.BigEndian.PutUint32(resp[4:8], 0x11223344) // epoch; old parser read resp[6:8] as 0x3344
	binary.BigEndian.PutUint16(resp[8:10], 0)         // internal
	binary.BigEndian.PutUint16(resp[10:12], 53186)    // mapped external
	binary.BigEndian.PutUint32(resp[12:16], 60)

	internal, external, lifetime, err := parseMapResponse(opMapUDP, resp)
	if err != nil {
		t.Fatal(err)
	}
	if internal != 0 {
		t.Errorf("internal=%d want 0", internal)
	}
	if external != 53186 {
		t.Errorf("external=%d want 53186", external)
	}
	if lifetime != 60 {
		t.Errorf("lifetime=%d want 60", lifetime)
	}
	epochAsPort := int(binary.BigEndian.Uint16(resp[6:8]))
	if external == epochAsPort {
		t.Fatalf("parsed epoch bytes as port (%d)", epochAsPort)
	}
}

func TestEncodeMapRequestProtonShape(t *testing.T) {
	req := encodeMapRequest(opMapUDP, suggestedPublicPort, 0, protonLifetime)
	if len(req) != 12 {
		t.Fatalf("len=%d", len(req))
	}
	if req[1] != opMapUDP {
		t.Errorf("op=%d", req[1])
	}
	if binary.BigEndian.Uint16(req[4:6]) != 0 {
		t.Errorf("private port=%d want 0", binary.BigEndian.Uint16(req[4:6]))
	}
	if binary.BigEndian.Uint16(req[6:8]) != suggestedPublicPort {
		t.Errorf("suggested public=%d want %d", binary.BigEndian.Uint16(req[6:8]), suggestedPublicPort)
	}
	if binary.BigEndian.Uint32(req[8:12]) != protonLifetime {
		t.Errorf("lifetime=%d", binary.BigEndian.Uint32(req[8:12]))
	}
}

func TestNatPMPGatewayFromDNS(t *testing.T) {
	cfg := parseConfig("[Interface]\nDNS = 10.2.0.1\n")
	if g := natPMPGateway(cfg); g != "10.2.0.1" {
		t.Errorf("got %s", g)
	}
	if g := natPMPGateway(parsedConfig{}); g != "10.2.0.1" {
		t.Errorf("empty cfg got %s", g)
	}
}

func TestNatPmpStartNoGateway(t *testing.T) {
	n := newNATPMPClient()
	if err := n.start("", "127.0.0.1"); err == nil {
		t.Fatal("expected error when no gateway is set")
	}
	if _, err := n.exchange(encodePublicAddressRequest(), 12); err == nil {
		t.Fatal("expected exchange error without gateway")
	}
}

func TestNatPmpUDPAndTCPMap(t *testing.T) {
	const mapped = 53186
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	gwPort := pc.LocalAddr().(*net.UDPAddr).Port

	go func() {
		buf := make([]byte, 32)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 2 {
				continue
			}
			op := buf[1]
			switch op {
			case opPublicAddr:
				resp := make([]byte, 12)
				resp[1] = 128 + opPublicAddr
				copy(resp[8:12], net.IPv4(79, 127, 147, 83).To4())
				_, _ = pc.WriteTo(resp, addr)
			case opMapUDP, opMapTCP:
				resp := make([]byte, 16)
				resp[1] = 128 + op
				binary.BigEndian.PutUint16(resp[10:12], mapped)
				binary.BigEndian.PutUint32(resp[12:16], protonLifetime)
				_, _ = pc.WriteTo(resp, addr)
			}
		}
	}()

	n := newNATPMPClient()
	n.gatewayPort = gwPort
	if err := n.start("127.0.0.1", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	defer n.stop()

	enabled, internal, external, proto, lifetime, _, gw := n.status()
	if !enabled {
		t.Fatal("expected enabled")
	}
	if internal != mapped || external != mapped {
		t.Errorf("ports internal=%d external=%d want %d", internal, external, mapped)
	}
	if proto != "udp+tcp" {
		t.Errorf("protocol=%s", proto)
	}
	if lifetime != protonLifetime {
		t.Errorf("lifetime=%d", lifetime)
	}
	if gw != "127.0.0.1" {
		t.Errorf("gateway=%s", gw)
	}
}

func TestApplyListenConfigIPv4DisablesIPv6(t *testing.T) {
	cfg := torrent.NewDefaultClientConfig()
	applyListenConfig(cfg, "10.2.0.2", 53186)
	if cfg.ListenPort != 53186 {
		t.Errorf("ListenPort=%d", cfg.ListenPort)
	}
	if !cfg.DisableIPv6 {
		t.Fatal("expected DisableIPv6")
	}
	if !cfg.NoDefaultPortForwarding {
		t.Fatal("expected NoDefaultPortForwarding")
	}
	if got := cfg.ListenHost("tcp4"); got != "10.2.0.2" {
		t.Errorf("tcp4 host=%s", got)
	}
	if got := cfg.ListenHost("tcp6"); got != "" {
		t.Errorf("tcp6 host=%s want empty", got)
	}
}

func TestRebindKeepsClientOnFailure(t *testing.T) {
	eng, err := newAnacrolixEngineOpts(t.TempDir(), 0, nil, anacrolixEngineOpts{
		ListenHost: "127.0.0.1",
		EnableDHT:  false,
		EnablePEX:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	old := eng.client
	if old == nil {
		t.Fatal("expected client")
	}
	if err := eng.rebind("8.8.8.8", 9); err == nil {
		t.Fatal("expected rebind to fail")
	}
	if eng.client != old {
		t.Fatal("client replaced after failed rebind")
	}
	if eng.listenHost != "127.0.0.1" {
		t.Errorf("listenHost=%s", eng.listenHost)
	}
}

func TestRebindUpdatesHostAndPort(t *testing.T) {
	eng, err := newAnacrolixEngineOpts(t.TempDir(), 0, nil, anacrolixEngineOpts{
		EnableDHT: false,
		EnablePEX: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	old := eng.client
	if err := eng.rebind("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	if eng.client == old {
		t.Fatal("expected new client")
	}
	if eng.listenHost != "127.0.0.1" {
		t.Errorf("listenHost=%s", eng.listenHost)
	}
}

func TestPieceCompletionOpensAfterCleanClose(t *testing.T) {
	dir := t.TempDir()
	opts := anacrolixEngineOpts{EnableDHT: false, EnablePEX: false, ListenHost: "127.0.0.1"}
	eng, err := newAnacrolixEngineOpts(dir, 0, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	eng2, err := newAnacrolixEngineOpts(dir, 0, nil, opts)
	if err != nil {
		t.Fatalf("second engine on same dir: %v", err)
	}
	defer eng2.Close()
}

func TestRebindReusesPieceCompletion(t *testing.T) {
	eng, err := newAnacrolixEngineOpts(t.TempDir(), 0, nil, anacrolixEngineOpts{
		EnableDHT: false, EnablePEX: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	st := eng.storage
	if err := eng.rebind("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	if eng.storage != st {
		t.Fatal("rebind must keep the same piece-completion storage")
	}
}