package core

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/common/format"
	"github.com/wyx2685/v2node/conf"
	"github.com/wyx2685/v2node/core/dualtls"
	"github.com/wyx2685/v2node/limiter"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	xcore "github.com/xtls/xray-core/core"
	xconf "github.com/xtls/xray-core/infra/conf"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
)

func testTLSCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "dualtls.test"}, DNSNames: []string{"dualtls.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cp, kp := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(cp, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return cp, kp, pool
}

func testEcho(t *testing.T, prefix string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(8 * time.Second))
				if prefix != "" {
					_, _ = c.Write([]byte(prefix))
				}
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}
func testFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func testProtocolClient(t *testing.T, protocol, credential, cert string, port int) *xcore.Instance {
	t.Helper()
	var settings any
	if protocol == "anytls" {
		settings = map[string]any{"address": "127.0.0.1", "port": port, "password": credential}
	} else {
		settings = map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"id": credential, "encryption": "none"}}}}}
	}
	body, err := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "error"}, "outbounds": []any{map[string]any{"protocol": protocol, "settings": settings, "streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"serverName": "dualtls.test", "allowInsecure": false, "disableSystemRoot": true, "certificates": []any{map[string]any{"certificateFile": cert, "usage": "verify"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var c xconf.Config
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	config, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := xcore.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

func protocolExchange(client *xcore.Instance, dest xnet.Destination, payload, expected []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := xcore.Dial(ctx, client, dest)
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		if _, err := conn.Write(payload); err != nil {
			done <- err
			return
		}
		got := make([]byte, len(expected))
		_, err := io.ReadFull(conn, got)
		if err == nil && !bytes.Equal(got, expected) {
			err = fmt.Errorf("response differs")
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestDualTLSOneListenerBothProtocolsAndAccounting(t *testing.T) {
	cert, key, roots := testTLSCertificate(t)
	echo := testEcho(t, "")
	childEcho := testEcho(t, "child:")
	port := testFreePort(t)
	childOut := fmt.Sprintf(`{"tag":"child-out","protocol":"freedom","settings":{"redirect":%q,"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}`, childEcho.Addr().String())
	parentOut := `{"tag":"test-parent","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}`
	info := &panel.NodeInfo{Id: 25, Type: "anytls", Security: panel.Tls, Tag: "dual-parent-25", Common: &panel.CommonNode{DualTLS: true, ListenIP: "127.0.0.1", ServerPort: port, Network: "tcp", Tls: panel.Tls, Routes: []panel.Route{{Id: 2, Action: "default_out", ActionValue: &parentOut}}, CertInfo: &panel.CertInfo{CertMode: "file", CertFile: cert, KeyFile: key}, SharedChildren: []panel.SharedAnyTLSChild{{Id: 26, Routes: []panel.Route{{Id: 1, Action: "default_out", ActionValue: &childOut}}}}}}
	users := []panel.UserInfo{{Id: 7, Uuid: "11111111-1111-4111-8111-111111111111", SpeedLimit: 100, DeviceLimit: 1, SharedChildIDs: []int{26}}}
	// Include a real AnyTLS password whose auth hash starts with VLESS version 0.
	for n := 0; ; n++ {
		p := fmt.Sprintf("22222222-2222-4222-8222-%012d", n)
		sum := sha256.Sum256([]byte(p))
		if sum[0] == 0 {
			users = append(users, panel.UserInfo{Id: 8, Uuid: p})
			break
		}
	}
	limiter.Init()
	limit := limiter.AddLimiter("anytls", info.Tag, users, map[int]int{})
	t.Cleanup(func() { limiter.DeleteLimiter(info.Tag) })
	server := New(conf.New())
	if err := server.Start([]*panel.NodeInfo{info}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	inbound, err := buildInbound(info, info.Tag)
	if err != nil {
		t.Fatal(err)
	}
	receiverConfig, err := inbound.ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	receiver := receiverConfig.(*proxyman.ReceiverConfig)
	if len(receiver.StreamSettings.SecuritySettings) != 1 {
		t.Fatal("expected one ordinary TLS configuration")
	}
	tlsConfig, err := receiver.StreamSettings.SecuritySettings[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	// These temporary certificates never rotate. The pinned Xray certificate
	// reloader writes its shared certificate slice without synchronization,
	// including on its initial OCSP refresh. Keep that unrelated upstream race
	// out of this protocol/accounting test by using an immutable test fixture.
	// Production certificate reload and OCSP settings remain unchanged.
	for _, certificate := range tlsConfig.(*xtls.Config).Certificate {
		certificate.OneTimeLoading = true
	}
	receiver.StreamSettings.SecuritySettings[0] = serial.ToTypedMessage(tlsConfig)
	inbound.ReceiverSettings = serial.ToTypedMessage(receiver)
	if err := server.addInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&AddUsersParams{Tag: info.Tag, NodeInfo: info, Users: users}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond) // pinned AnyTLS implementation batches auth changes
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dest, _ := xnet.ParseDestination("tcp:" + echo.Addr().String())
	payload := bytes.Repeat([]byte("same-port-"), 4096)
	for _, protocol := range []string{"anytls", "vless"} {
		t.Run(protocol, func(t *testing.T) {
			client := testProtocolClient(t, protocol, users[0].Uuid, cert, port)
			if err := protocolExchange(client, dest, payload, payload); err != nil {
				t.Fatal(err)
			}
			traffic, err := server.GetUserTrafficSlice(info.Tag, 0)
			if err != nil || len(traffic) != 1 || traffic[0].UID != 7 || traffic[0].Upload < int64(len(payload)) || traffic[0].Download < int64(len(payload)) {
				t.Fatalf("canonical accounting lost: %+v %v", traffic, err)
			}
		})
	}
	t.Run("udp-both-protocols", func(t *testing.T) {
		udp, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer udp.Close()
		go func() {
			b := make([]byte, 65535)
			for {
				n, peer, err := udp.ReadFrom(b)
				if err != nil {
					return
				}
				_, _ = udp.WriteTo(b[:n], peer)
			}
		}()
		dest, _ := xnet.ParseDestination("udp:" + udp.LocalAddr().String())
		for _, p := range []string{"anytls", "vless"} {
			client := testProtocolClient(t, p, users[0].Uuid, cert, port)
			payload, expected := []byte("udp-same-port"), []byte("udp-same-port")
			requestDest := dest
			if p == "anytls" {
				// Use sing's standard UoT encoding over an AnyTLS TCP stream.
				// The pinned Xray AnyTLS outbound erroneously parses response
				// packets as requests when its own st.isUDP path is selected.
				request, err := uot.EncodeRequest(uot.Request{IsConnect: true, Destination: M.ParseSocksaddr(udp.LocalAddr().String())})
				if err != nil {
					t.Fatal(err)
				}
				wire := append([]byte(nil), request.Bytes()...)
				request.Release()
				wire = binary.BigEndian.AppendUint16(wire, uint16(len(payload)))
				payload = append(wire, payload...)
				expected = append(binary.BigEndian.AppendUint16(nil, uint16(len(expected))), expected...)
				requestDest, _ = xnet.ParseDestination("tcp:" + uot.MagicAddress + ":443")
			}
			if err := protocolExchange(client, requestDest, payload, expected); err != nil {
				t.Fatalf("%s UDP: %v", p, err)
			}
		}
	})
	t.Run("anytls-zero-prefix", func(t *testing.T) {
		client := testProtocolClient(t, "anytls", users[1].Uuid, cert, port)
		if err := protocolExchange(client, dest, []byte("zero"), []byte("zero")); err != nil {
			t.Fatal(err)
		}
	})
	for _, protocol := range []string{"anytls", "vless"} {
		t.Run("child-"+protocol, func(t *testing.T) {
			password := format.SharedChildPassword(users[0].Uuid, 26)
			credential := password
			if protocol == "vless" {
				id := dualtls.CredentialUUID(password)
				credential = id.String()
			}
			client := testProtocolClient(t, protocol, credential, cert, port)
			if err := protocolExchange(client, dest, []byte("route"), []byte("child:route")); err != nil {
				t.Fatal(err)
			}
			traffic, err := server.GetUserTrafficSlice(info.Tag, 0)
			found := false
			for _, tr := range traffic {
				if tr.UID == 7 && tr.Upload > 0 && tr.Download > 0 {
					found = true
				}
			}
			if err != nil || !found {
				t.Fatalf("child billing lost: %+v %v", traffic, err)
			}
		})
	}
	t.Run("fragmented-short-vless", func(t *testing.T) {
		conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, ServerName: "dualtls.test", MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		id := dualtls.CredentialUUID(users[0].Uuid)
		header := append([]byte{0}, id[:]...)
		header = append(header, 0, 1)
		targetPort := echo.Addr().(*net.TCPAddr).Port
		header = binary.BigEndian.AppendUint16(header, uint16(targetPort))
		header = append(header, 1, 127, 0, 0, 1)
		for _, b := range header {
			if _, err := conn.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
		}
		conn.Write([]byte("x"))
		response := make([]byte, 3)
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(response, []byte{0, 0, 'x'}) {
			t.Fatalf("bad VLESS response %x", response)
		}
	})
	t.Run("concurrent-protocols", func(t *testing.T) {
		a := testProtocolClient(t, "anytls", users[0].Uuid, cert, port)
		v := testProtocolClient(t, "vless", users[0].Uuid, cert, port)
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				c := a
				if i%2 == 0 {
					c = v
				}
				errs <- protocolExchange(c, dest, []byte("mixed"), []byte("mixed"))
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
	})
	t.Run("wrong-auth", func(t *testing.T) {
		for _, p := range []string{"anytls", "vless"} {
			client := testProtocolClient(t, p, "99999999-9999-4999-8999-999999999999", cert, port)
			if err := protocolExchange(client, dest, []byte("deny"), []byte("deny")); err == nil {
				t.Fatalf("%s accepted wrong credentials", p)
			}
		}
	})
	t.Run("tls-name-verified", func(t *testing.T) {
		c, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, ServerName: "wrong.test"})
		if err == nil {
			c.Close()
			t.Fatal("wrong TLS name accepted")
		}
	})
	t.Run("revoke-both", func(t *testing.T) {
		persistent := testProtocolClient(t, "anytls", users[0].Uuid, cert, port)
		if err := protocolExchange(persistent, dest, []byte("before"), []byte("before")); err != nil {
			t.Fatal(err)
		}
		var buckets int
		limit.SpeedLimiter.Range(func(key, value any) bool {
			if key == format.UserTag(info.Tag, users[0].Uuid) {
				buckets++
			}
			return true
		})
		if buckets != 1 {
			t.Fatalf("protocols did not share one speed bucket: %d", buckets)
		}
		if err := server.DelUsers(users[:1], info.Tag, info); err != nil {
			t.Fatal(err)
		}
		limit.UpdateUser(info.Tag, nil, users[:1], nil)
		time.Sleep(250 * time.Millisecond)
		for _, p := range []string{"anytls", "vless"} {
			client := testProtocolClient(t, p, users[0].Uuid, cert, port)
			if err := protocolExchange(client, dest, []byte("deny"), []byte("deny")); err == nil {
				t.Fatalf("%s accepted revoked user", p)
			}
		}
		if err := protocolExchange(persistent, dest, []byte("deny"), []byte("deny")); err == nil {
			t.Fatal("revoked user reused authenticated AnyTLS session")
		}
	})
}

func TestDualTLSRejectsIncompatibleTransport(t *testing.T) {
	for _, tc := range []struct {
		kind     string
		security int
		network  string
		mode     string
	}{{"vless", panel.Tls, "tcp", "file"}, {"anytls", panel.Reality, "tcp", "file"}, {"anytls", panel.Tls, "ws", "file"}, {"anytls", panel.Tls, "tcp", "none"}} {
		info := &panel.NodeInfo{Type: tc.kind, Security: tc.security, Common: &panel.CommonNode{DualTLS: true, ListenIP: "127.0.0.1", ServerPort: 12345, Network: tc.network, CertInfo: &panel.CertInfo{CertMode: tc.mode, CertFile: "unused", KeyFile: "unused"}}}
		if _, err := buildInbound(info, "invalid"); err == nil {
			t.Errorf("incompatible transport accepted: %+v", tc)
		}
	}
}
