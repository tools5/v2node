// Package dualtls serves AnyTLS and ordinary VLESS through one TLS receiver.
package dualtls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/anytls"
	"github.com/xtls/xray-core/proxy/vless"
	vlessin "github.com/xtls/xray-core/proxy/vless/inbound"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
	"google.golang.org/protobuf/proto"
)

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, c interface{}) (interface{}, error) {
		cfg := new(anytls.ServerConfig)
		if err := proto.Unmarshal(c.(*Config).AnytlsSettings, cfg); err != nil {
			return nil, err
		}
		users := cfg.Users
		cfg.Users = nil
		a, err := anytls.NewServer(ctx, cfg)
		if err != nil {
			return nil, err
		}
		raw, err := common.CreateObject(ctx, &vlessin.Config{Decryption: "none"})
		if err != nil {
			a.Close()
			return nil, err
		}
		h := &Handler{anytls: a, vless: raw.(*vlessin.Handler), users: make(map[string]*protocol.MemoryUser), ids: make(map[uuid.UUID]string), normalized: make(map[[16]byte]string), hashes: make(map[[17]byte]string), policy: core.MustFromContext(ctx).GetFeature(policy.ManagerType()).(policy.Manager)}
		for _, user := range users {
			mu, err := user.ToMemoryUser()
			if err == nil {
				err = h.AddUser(ctx, mu)
			}
			if err != nil {
				h.Close()
				return nil, err
			}
		}
		return h, nil
	}))
}

type Handler struct {
	anytls     *anytls.Server
	vless      *vlessin.Handler
	policy     policy.Manager
	mu         sync.RWMutex
	users      map[string]*protocol.MemoryUser
	ids        map[uuid.UUID]string
	normalized map[[16]byte]string
	hashes     map[[17]byte]string
	once       sync.Once
	closeErr   error
}

// CredentialUUID preserves panel UUIDs. Shared-child HMAC passwords get a
// domain-separated deterministic UUID so their routing identity stays distinct.
func CredentialUUID(password string) uuid.UUID {
	compact := strings.ReplaceAll(password, "-", "")
	if len(compact) == 32 && (len(password) == 32 || len(password) == 36) {
		if b, err := hex.DecodeString(compact); err == nil {
			id, _ := uuid.ParseBytes(b)
			return id
		}
	}
	sum := sha256.Sum256([]byte("v2nb:anytls-vless:v1:" + password))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 15) | 0x50
	id[8] = (id[8] & 63) | 0x80
	return id
}

func authPrefix(password string) [17]byte {
	sum := sha256.Sum256([]byte(password))
	var prefix [17]byte
	copy(prefix[:], sum[:17])
	return prefix
}
func vlessPrefix(id uuid.UUID) [17]byte { var p [17]byte; copy(p[1:], id[:]); return p }

func (h *Handler) AddUser(ctx context.Context, user *protocol.MemoryUser) error {
	if user == nil || user.Email == "" {
		return errors.New("dualtls: missing user identity")
	}
	acc, ok := user.Account.(*anytls.MemoryAccount)
	if !ok || acc.Password == "" {
		return errors.New("dualtls: expected AnyTLS account")
	}
	id := CredentialUUID(acc.Password)
	hash := authPrefix(acc.Password)
	key := strings.ToLower(user.Email)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.users[key] != nil {
		return errors.New("dualtls: duplicate user identity")
	}
	if _, ok := h.normalized[vless.ProcessUUID(id)]; ok {
		return errors.New("dualtls: conflicting VLESS credential")
	}
	if _, ok := h.hashes[hash]; ok {
		return errors.New("dualtls: conflicting AnyTLS credential")
	}
	// A credential must never ambiguously authenticate as the other protocol.
	if hash == vlessPrefix(id) {
		return errors.New("dualtls: ambiguous credential")
	}
	if _, ok := h.hashes[vlessPrefix(id)]; ok {
		return errors.New("dualtls: ambiguous VLESS credential")
	}
	if hash[0] == 0 {
		var other uuid.UUID
		copy(other[:], hash[1:])
		if _, ok := h.ids[other]; ok {
			return errors.New("dualtls: ambiguous AnyTLS credential")
		}
	}
	vu := &protocol.MemoryUser{Email: user.Email, Level: user.Level, Account: &vless.MemoryAccount{ID: protocol.NewID(id), Encryption: "none"}}
	if err := h.vless.AddUser(ctx, vu); err != nil {
		return err
	}
	if err := h.anytls.AddUser(ctx, user); err != nil {
		_ = h.vless.RemoveUser(ctx, user.Email)
		return err
	}
	h.users[key] = user
	h.ids[id] = key
	h.normalized[vless.ProcessUUID(id)] = key
	h.hashes[hash] = key
	return nil
}

func (h *Handler) RemoveUser(ctx context.Context, email string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := strings.ToLower(email)
	user := h.users[key]
	if user == nil {
		return fmt.Errorf("dualtls: unknown user")
	}
	password := user.Account.(*anytls.MemoryAccount).Password
	id := CredentialUUID(password)
	err := errors.Join(h.vless.RemoveUser(ctx, user.Email), h.anytls.RemoveUser(ctx, user.Email))
	delete(h.users, key)
	delete(h.ids, id)
	delete(h.normalized, vless.ProcessUUID(id))
	delete(h.hashes, authPrefix(password))
	return err
}
func (h *Handler) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.users[strings.ToLower(email)]
}
func (h *Handler) GetUsers(_ context.Context) []*protocol.MemoryUser {
	h.mu.RLock()
	defer h.mu.RUnlock()
	users := make([]*protocol.MemoryUser, 0, len(h.users))
	for _, u := range h.users {
		users = append(users, u)
	}
	return users
}
func (h *Handler) GetUsersCount(_ context.Context) int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return int64(len(h.users))
}
func (h *Handler) Network() []xnet.Network { return []xnet.Network{xnet.Network_TCP} }
func (h *Handler) Close() error {
	h.once.Do(func() { h.closeErr = errors.Join(h.anytls.Close(), h.vless.Close()) })
	return h.closeErr
}

type replayConn struct {
	net.Conn
	reader            io.Reader
	handshakeDeadline time.Time
}

func (c *replayConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func (c *replayConn) SetReadDeadline(t time.Time) error {
	if !t.IsZero() && !c.handshakeDeadline.IsZero() && t.After(c.handshakeDeadline) {
		t = c.handshakeDeadline
	}
	if t.IsZero() {
		c.handshakeDeadline = time.Time{}
	}
	return c.Conn.SetReadDeadline(t)
}

func (h *Handler) isVLESS(p [17]byte) bool {
	if p[0] != 0 {
		return false
	}
	var id uuid.UUID
	copy(id[:], p[1:])
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.ids[id]
	return ok
}

func (h *Handler) Process(ctx context.Context, network xnet.Network, conn stat.Connection, dispatcher routing.Dispatcher) error {
	// Restrict this adapter to an actual TLS receiver. Vision/Reality and cleartext
	// require their original connection types and are deliberately rejected.
	if _, ok := stat.TryUnwrapStatsConn(conn).(*tls.Conn); !ok {
		return errors.New("dualtls: ordinary TLS receiver required")
	}
	if network != xnet.Network_TCP {
		return errors.New("dualtls: TCP required")
	}
	deadline := time.Now().Add(h.policy.ForLevel(0).Timeouts.Handshake)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	var prefix [17]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return err
	}
	replay := &replayConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(prefix[:]), conn), handshakeDeadline: deadline}
	if h.isVLESS(prefix) {
		return h.vless.Process(ctx, network, replay, dispatcher)
	}
	return h.anytls.Process(ctx, network, replay, tlsDispatcher{Dispatcher: dispatcher})
}

// AnyTLS marks unknown connection wrappers as cleartext. Here the concrete TLS
// receiver was checked above; remove only that synthetic attribute on dispatch.
type tlsDispatcher struct{ routing.Dispatcher }

func tlsContext(ctx context.Context) context.Context {
	content := session.ContentFromContext(ctx)
	if content == nil || content.Attribute("anytls") != "notls" {
		return ctx
	}
	clone := *content
	clone.Attributes = make(map[string]string, len(content.Attributes))
	for k, v := range content.Attributes {
		if k != "anytls" {
			clone.Attributes[k] = v
		}
	}
	return session.ContextWithContent(ctx, &clone)
}
func (d tlsDispatcher) Dispatch(ctx context.Context, dest xnet.Destination) (*transport.Link, error) {
	return d.Dispatcher.Dispatch(tlsContext(ctx), dest)
}
func (d tlsDispatcher) DispatchLink(ctx context.Context, dest xnet.Destination, link *transport.Link) error {
	return d.Dispatcher.DispatchLink(tlsContext(ctx), dest, link)
}

var _ proxy.Inbound = (*Handler)(nil)
var _ proxy.UserManager = (*Handler)(nil)
