package netmap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PeeringDB (https://www.peeringdb.com) is where networks tell each other where they can be met:
// exchange points (IX), data centres, who is present where, with what port speed and address in
// the peering LAN of an IX. Its policy forbids using the data for commerce; only technical data is
// shown on the map, never the contacts.
const DefaultPeeringDB = "https://www.peeringdb.com/api"

// PeeringDB is the part of PeeringDB the map uses.
type PeeringDB struct {
	Networks   []PDBNetwork
	IXs        []PDBIX
	Facilities []PDBFacility
	Ports      []PDBPort     // netixlan: a network's port at an exchange point
	Presence   []PDBPresence // netfac: a network in a data centre
	IXFacility []PDBIXFacility
	LANs       []PDBLAN    // ixlan: the peering LAN of an exchange point
	LANPrefix  []PDBPrefix // ixpfx: the addresses of such a LAN
}

type PDBNetwork struct {
	ASN     uint32 `json:"asn"`
	Name    string `json:"name"`
	Type    string `json:"info_type"`    // «NSP», «Content», «Cable/DSL/ISP», «Enterprise»…
	Scope   string `json:"info_scope"`   // «Global», «Europe», «Regional»…
	Traffic string `json:"info_traffic"` // «1-5Tbps»…
	Policy  string `json:"policy_general"`
	Website string `json:"website"`
}

type PDBIX struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Long    string `json:"name_long"`
	City    string `json:"city"`
	Country string `json:"country"`
	Website string `json:"website"`
}

type PDBFacility struct {
	ID      int      `json:"id"`
	Name    string   `json:"name"`
	City    string   `json:"city"`
	Country string   `json:"country"`
	Lat     *float64 `json:"latitude"`
	Lon     *float64 `json:"longitude"`
}

type PDBPort struct {
	ASN         uint32 `json:"asn"`
	IX          int    `json:"ix_id"`
	LAN         int    `json:"ixlan_id"`
	Speed       int64  `json:"speed"` // Mbit/s, a bundle of ports counts as one
	IPv4        string `json:"ipaddr4"`
	IPv6        string `json:"ipaddr6"`
	RouteServer bool   `json:"is_rs_peer"`
	Operational bool   `json:"operational"`
}

type PDBPresence struct {
	ASN      uint32 `json:"local_asn"`
	Facility int    `json:"fac_id"`
}

type PDBIXFacility struct {
	IX       int `json:"ix_id"`
	Facility int `json:"fac_id"`
}

type PDBLAN struct {
	ID int `json:"id"`
	IX int `json:"ix_id"`
}

type PDBPrefix struct {
	LAN    int    `json:"ixlan_id"`
	Prefix string `json:"prefix"`
}

// pdbObjects are the lists fetched, with only the fields the map reads.
var pdbObjects = []struct {
	name, fields string
}{
	{"net", "asn,name,info_type,info_scope,info_traffic,policy_general,website"},
	{"ix", "id,name,name_long,city,country,website"},
	{"fac", "id,name,city,country,latitude,longitude"},
	{"netixlan", "asn,ix_id,ixlan_id,speed,ipaddr4,ipaddr6,is_rs_peer,operational"},
	{"netfac", "local_asn,fac_id"},
	{"ixfac", "ix_id,fac_id"},
	{"ixlan", "id,ix_id"},
	{"ixpfx", "ixlan_id,prefix"},
}

// PeeringDBPause is the pause between two requests: PeeringDB answers anonymous clients 20 times
// a minute.
const PeeringDBPause = 4 * time.Second

// FetchPeeringDB saves every list the map needs into dir as <object>.json — one request each, with
// a pause between them. apiKey is optional and raises the limit.
func FetchPeeringDB(ctx context.Context, client *http.Client, base, apiKey, userAgent, dir string, pause time.Duration) error {
	if base == "" {
		base = DefaultPeeringDB
	}
	for i, object := range pdbObjects {
		if i > 0 && pause > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}
		url := fmt.Sprintf("%s/%s?depth=0&limit=0&fields=%s", strings.TrimSuffix(base, "/"), object.name, object.fields)
		if err := download(ctx, client, url, userAgent, apiKey, filepath.Join(dir, object.name+".json")); err != nil {
			return fmt.Errorf("peeringdb %s: %w", object.name, err)
		}
	}
	return nil
}

// LoadPeeringDB reads what FetchPeeringDB saved.
func LoadPeeringDB(dir string) (*PeeringDB, error) {
	db := &PeeringDB{}
	targets := map[string]any{
		"net": &db.Networks, "ix": &db.IXs, "fac": &db.Facilities, "netixlan": &db.Ports,
		"netfac": &db.Presence, "ixfac": &db.IXFacility, "ixlan": &db.LANs, "ixpfx": &db.LANPrefix,
	}
	for _, object := range pdbObjects {
		file, err := os.Open(filepath.Join(dir, object.name+".json"))
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		err = json.NewDecoder(file).Decode(&envelope)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("peeringdb %s: %w", object.name, err)
		}
		if len(envelope.Data) == 0 {
			return nil, fmt.Errorf("peeringdb %s: no data", object.name)
		}
		if err := json.Unmarshal(envelope.Data, targets[object.name]); err != nil {
			return nil, fmt.Errorf("peeringdb %s: %w", object.name, err)
		}
	}
	return db, nil
}

// LANPrefixes lists the peering LANs of exchange points with the IX each belongs to: an address
// of a traceroute inside one of them is a router's port at that IX.
func (db *PeeringDB) LANPrefixes() map[netip.Prefix]int {
	lanIX := make(map[int]int, len(db.LANs))
	for _, lan := range db.LANs {
		lanIX[lan.ID] = lan.IX
	}
	out := make(map[netip.Prefix]int, len(db.LANPrefix))
	for _, p := range db.LANPrefix {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(p.Prefix))
		if ix, ok := lanIX[p.LAN]; ok && err == nil {
			out[prefix.Masked()] = ix
		}
	}
	return out
}

// download saves url to path through a temporary file: a half-written file never replaces a good one.
func download(ctx context.Context, client *http.Client, url, userAgent, apiKey, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)
	if apiKey != "" {
		request.Header.Set("Authorization", "Api-Key "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%s: HTTP %d", url, response.StatusCode)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = io.Copy(temporary, response.Body)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary.Name())
		return err
	}
	return os.Rename(temporary.Name(), path)
}
