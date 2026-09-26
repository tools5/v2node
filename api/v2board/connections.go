package panel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/wyx2685/v2node/common/connectioncontrol"
)

const connectionEndpoint = "/api/v1/server/UniProxy/connections"

// ConnectionTransport has a separate process identity and bounded HTTP
// responses. It reuses the existing node authentication and HTTP transport.
type ConnectionTransport struct {
	panel      *Client
	instanceID string
	client     *http.Client
	once       sync.Once
	initErr    error
}

func NewConnectionTransport(client *Client) *ConnectionTransport {
	return &ConnectionTransport{panel: client}
}

func (t *ConnectionTransport) initialize() error {
	t.once.Do(func() {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			t.initErr = fmt.Errorf("cannot initialize connection monitor identity")
			return
		}
		t.instanceID = hex.EncodeToString(id[:])
		t.client = &http.Client{
			Transport: t.panel.client.GetClient().Transport,
			// A node token must not be forwarded to a redirect target.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	})
	return t.initErr
}

func (t *ConnectionTransport) request(ctx context.Context, method string, body io.Reader) (*http.Response, error) {
	if err := t.initialize(); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimRight(t.panel.APIHost, "/") + connectionEndpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid panel address for connection monitor")
	}
	query := u.Query()
	query.Set("node_type", "v2node")
	query.Set("node_id", fmt.Sprint(t.panel.NodeId))
	query.Set("token", t.panel.Token)
	query.Set("version", fmt.Sprint(connectioncontrol.Version))
	query.Set("instance_id", t.instanceID)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("cannot create connection monitor request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "v2nb-connections/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := t.client.Do(req)
	if err != nil {
		// net/http errors can embed the full URL, including the node token.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("connection monitor transport failed")
	}
	return response, nil
}

func (t *ConnectionTransport) PollConnectionCommands(ctx context.Context) (connectioncontrol.Batch, error) {
	var batch connectioncontrol.Batch
	response, err := t.request(ctx, http.MethodGet, nil)
	if err != nil {
		return batch, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented {
		return batch, connectioncontrol.ErrUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return batch, fmt.Errorf("connection monitor poll returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data connectioncontrol.Batch `json:"data"`
	}
	if err := decodeConnectionJSON(response.Body, &envelope, 64<<10); err != nil {
		return batch, err
	}
	return envelope.Data, nil
}

func (t *ConnectionTransport) SubmitConnectionResult(ctx context.Context, result connectioncontrol.Result) error {
	if err := t.initialize(); err != nil {
		return err
	}
	if len(result.Connections) > connectioncontrol.MaxConnections {
		return connectioncontrol.ErrInvalid
	}
	payload := struct {
		connectioncontrol.Result
		InstanceID string `json:"instance_id"`
	}{result, t.instanceID}
	data, err := json.Marshal(payload)
	if err != nil {
		return connectioncontrol.ErrInvalid
	}
	if len(data) > 2<<20 {
		return connectioncontrol.ErrInvalid
	}
	response, err := t.request(ctx, http.MethodPost, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusConflict {
		return connectioncontrol.ErrExpired
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("connection monitor result returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data bool `json:"data"`
	}
	if err := decodeConnectionJSON(response.Body, &envelope, 16<<10); err != nil {
		return err
	}
	if !envelope.Data {
		return connectioncontrol.ErrInvalid
	}
	return nil
}

func decodeConnectionJSON(reader io.Reader, target any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit || json.Unmarshal(data, target) != nil {
		return connectioncontrol.ErrInvalid
	}
	return nil
}
