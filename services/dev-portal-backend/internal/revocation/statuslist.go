package revocation

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// StatusList is a status list as the portal shows it: decoded, not
// verified. The portal reads the list for a tester; a relying party checks
// the signature and the signer's certificate.
type StatusList struct {
	Provider string `json:"provider"`
	List     string `json:"list"`
	// URI is the list's sub: where the credentials say it is. Unread, it
	// is the address the portal fetched it from.
	URI string `json:"uri"`
	// Offers is how many offers of the portal use this list.
	Offers   int        `json:"offers"`
	IssuedAt *time.Time `json:"iat,omitempty"`
	Expires  *time.Time `json:"exp,omitempty"`
	// TTL is how many seconds a relying party may cache the list.
	TTL  int `json:"ttl,omitempty"`
	Bits int `json:"bits,omitempty"`
	// Size is the number of entries.
	Size int `json:"size,omitempty"`
	// Revoked are the entries whose status is not valid (not 0).
	Revoked []int `json:"revoked"`
	// Error is why the list could not be read; the other fields are then
	// empty.
	Error string `json:"error,omitempty"`
}

// StatusLists returns the status list of every provider and list the
// portal's offers use, sorted. A list that cannot be read has its error;
// the others are shown all the same.
func (s *Service) StatusLists(ctx context.Context) []StatusList {
	type key struct{ provider, list string }
	count := map[key]int{}
	for _, o := range s.Offers() {
		count[key{o.Provider, o.List}]++
	}
	keys := make([]key, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider != keys[j].provider {
			return keys[i].provider < keys[j].provider
		}
		return keys[i].list < keys[j].list
	})
	out := make([]StatusList, 0, len(keys))
	for _, k := range keys {
		uri := strings.TrimRight(s.cfg.StatusBaseURL, "/") + "/status/" + k.provider + "/" + k.list
		list := StatusList{Provider: k.provider, List: k.list, URI: uri, Offers: count[k], Revoked: []int{}}
		token, err := s.cfg.Fetch(ctx, uri)
		if err == nil {
			err = decodeStatusList(token, &list)
		}
		if err != nil {
			list = StatusList{Provider: k.provider, List: k.list, URI: uri, Offers: count[k], Revoked: []int{}, Error: err.Error()}
		}
		out = append(out, list)
	}
	return out
}

// maxListBytes bounds a decompressed list.
const maxListBytes = 8 << 20

// decodeStatusList reads a Status List Token in JWT format
// (draft-ietf-oauth-status-list section 5.1): iat, exp, ttl and the
// status_list, whose lst is the zlib-compressed byte array, base64url
// encoded, with the status of entry 0 in the least significant bits of the
// first byte (section 4.1).
func decodeStatusList(token string, list *StatusList) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	var claims struct {
		Subject    string `json:"sub"`
		IssuedAt   *int64 `json:"iat"`
		Expires    *int64 `json:"exp"`
		TTL        int    `json:"ttl"`
		StatusList struct {
			Bits int    `json:"bits"`
			Lst  string `json:"lst"`
		} `json:"status_list"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	bits := claims.StatusList.Bits
	if bits != 1 && bits != 2 && bits != 4 && bits != 8 {
		return fmt.Errorf("bits is %d, not 1, 2, 4 or 8", bits)
	}
	raw, err := base64.RawURLEncoding.DecodeString(claims.StatusList.Lst)
	if err != nil {
		return fmt.Errorf("lst: %w", err)
	}
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("lst: %w", err)
	}
	defer r.Close()
	statuses, err := io.ReadAll(io.LimitReader(r, maxListBytes+1))
	if err != nil {
		return fmt.Errorf("lst: %w", err)
	}
	if len(statuses) > maxListBytes {
		return errors.New("lst is too large")
	}
	// The token names the URI the credentials point to, which is the
	// issuer's public address rather than the one the portal fetched.
	if claims.Subject != "" {
		list.URI = claims.Subject
	}
	list.Bits = bits
	list.Size = len(statuses) * 8 / bits
	list.TTL = claims.TTL
	if claims.IssuedAt != nil {
		t := time.Unix(*claims.IssuedAt, 0).UTC()
		list.IssuedAt = &t
	}
	if claims.Expires != nil {
		t := time.Unix(*claims.Expires, 0).UTC()
		list.Expires = &t
	}
	mask := byte(1<<bits - 1)
	for i := 0; i < list.Size; i++ {
		position := i * bits
		if statuses[position/8]>>(position%8)&mask != 0 {
			list.Revoked = append(list.Revoked, i)
		}
	}
	return nil
}

// HTTPStatusFetcher fetches a Status List Token with client, asking for the
// JWT form (section 8.1).
func HTTPStatusFetcher(client *http.Client) StatusFetcher {
	return func(ctx context.Context, uri string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/statuslist+jwt")
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("unreachable: %w", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("the issuer answered %d", resp.StatusCode)
		}
		return strings.TrimSpace(string(body)), nil
	}
}

// HTTPCatalog reads the offers the sources publish from url (the
// eudi-adapter's /eudi-offers.json).
type HTTPCatalog struct {
	URL    string
	Client *http.Client
}

func (c HTTPCatalog) Offers(ctx context.Context) ([]AttestationOffer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", c.URL, resp.StatusCode)
	}
	var offers []AttestationOffer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&offers); err != nil {
		return nil, fmt.Errorf("%s: %w", c.URL, err)
	}
	return offers, nil
}
