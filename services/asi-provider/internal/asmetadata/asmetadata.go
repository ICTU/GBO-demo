// Package asmetadata obtains the keys of the authorization server: from the
// jwks_uri in its metadata (RFC 8414; RFC 9068 section 4), and again when a
// token names a kid it does not know. It implements accesstoken.KeySource.
package asmetadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/asi-provider/internal/accesstoken"
)

// refetchInterval limits how often an unknown kid makes the ASIP fetch the
// keys of the authorization server again.
const refetchInterval = 10 * time.Second

type Keys struct {
	issuer string
	// internal is the base URL the ASIP reaches the authorization server on,
	// when that differs from the issuer (in compose, the issuer is the host
	// address the QTSP uses). Empty means the issuer itself.
	internal string
	client   *http.Client
	mu       sync.Mutex
	set      jose.JSONWebKeySet
	fetched  time.Time
}

// New returns the keys of the authorization server with issuer identifier
// issuer, fetched from internal when that is not empty.
func New(issuer, internal string) *Keys {
	return &Keys{issuer: issuer, internal: strings.TrimRight(internal, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

// MetadataURL returns where the metadata of the authorization server with
// issuer identifier issuer is: the well-known suffix goes between the host and
// the path of the issuer (RFC 8414 section 3.1).
func MetadataURL(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme == "" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("issuer %q is not a URL without query or fragment", issuer)
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server" + strings.TrimRight(u.Path, "/"), nil
}

// reach maps a URL of the authorization server to the address the ASIP
// reaches it on.
func (k *Keys) reach(url string) string {
	if k.internal == "" {
		return url
	}
	if rest, ok := strings.CutPrefix(url, k.issuer); ok {
		return k.internal + rest
	}
	return url
}

func (k *Keys) Keys(ctx context.Context, kid string) ([]jose.JSONWebKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if found := accesstoken.Lookup(k.set, kid); len(found) > 0 || time.Since(k.fetched) < refetchInterval {
		return found, nil
	}
	set, err := k.fetch(ctx)
	if err != nil {
		if len(k.set.Keys) == 0 {
			return nil, fmt.Errorf("%w: %v", accesstoken.ErrKeysUnavailable, err)
		}
		return nil, nil
	}
	k.set, k.fetched = set, time.Now()
	return accesstoken.Lookup(k.set, kid), nil
}

func (k *Keys) fetch(ctx context.Context) (jose.JSONWebKeySet, error) {
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	// The internal base stands in for the issuer, path included.
	base := k.issuer
	if k.internal != "" {
		base = k.internal
	}
	metadata, err := MetadataURL(base)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	if err := k.getJSON(ctx, metadata, &meta); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	if meta.Issuer != k.issuer || meta.JWKSURI == "" {
		return jose.JSONWebKeySet{}, errors.New("authorization server metadata has another issuer or no jwks_uri")
	}
	var set jose.JSONWebKeySet
	if err := k.getJSON(ctx, k.reach(meta.JWKSURI), &set); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	public := jose.JSONWebKeySet{}
	for _, key := range set.Keys {
		if key.IsPublic() {
			public.Keys = append(public.Keys, key)
		}
	}
	return public, nil
}

func (k *Keys) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
