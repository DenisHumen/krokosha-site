package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/netmap"
)

const netmapUsage = `Usage: krokosha-cli netmap <command> [flags]

The map of the internet of the /map page (docs/netmap.md).

  fetch    download the sources into the data directory: CAIDA AS Relationships (once a month),
           iptoasn, today's RouteViews snapshots, PeeringDB
  build    build the map from the data directory and write the overview the page downloads
  route    print the likely path between two addresses: krokosha-cli netmap route FROM TO
  serve    for development: answer /api/net/* and serve the overview from memory, without MySQL
           (the web dev server passes them on with NETMAP_DEV_API=http://127.0.0.1:8091)
`

// netmapUserAgent introduces the site to the sources.
const netmapUserAgent = "krokosha-site-netmap/1 (+https://github.com/DenisHumen/krokosha-site)"

func runNetmap(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(netmapUsage)
		return nil
	}
	command, args := args[0], args[1:]
	flags := flag.NewFlagSet("netmap "+command, flag.ContinueOnError)
	data := flags.String("data", envOr("NETMAP_DATA_DIR", "/var/lib/krokosha/netmap/data"), "the data directory (default: NETMAP_DATA_DIR)")
	geoip := flags.String("geoip", envOr("GEOIP_DB", "/var/lib/GeoIP/dbip-city-lite.mmdb"), "a MaxMind DB file with coordinates (default: GEOIP_DB)")
	collectors := flags.String("collectors", strings.Join(netmap.DefaultPlaces.Collectors, ","), "RouteViews collectors")
	out := flags.String("out", envOr("NETMAP_OUT_DIR", "/var/lib/krokosha/netmap/public"), "build: where the overview goes (default: NETMAP_OUT_DIR)")
	listen := flags.String("listen", "127.0.0.1:8091", "serve: the address to listen on")
	me := flags.String("me", "", "serve: the address /api/net/me answers with (a visitor from localhost has none)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	places := netmap.DefaultPlaces
	places.Collectors = splitList(*collectors)

	switch command {
	case "fetch":
		fetcher := &netmap.Fetcher{
			Places: places, Client: &http.Client{Timeout: 20 * time.Minute}, UserAgent: netmapUserAgent,
			PDBKey: os.Getenv("PEERINGDB_API_KEY"), PDBPause: netmap.PeeringDBPause,
			Log: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
		}
		return fetcher.FetchAll(ctx, *data)
	case "build":
		model, err := loadModel(*data, *geoip, places.Collectors)
		if err != nil {
			return err
		}
		return writeOverview(model, *out)
	case "route":
		if flags.NArg() != 2 {
			return errors.New("usage: krokosha-cli netmap route [flags] FROM TO")
		}
		from, errFrom := netip.ParseAddr(flags.Arg(0))
		to, errTo := netip.ParseAddr(flags.Arg(1))
		if err := errors.Join(errFrom, errTo); err != nil {
			return err
		}
		model, err := loadModel(*data, *geoip, places.Collectors)
		if err != nil {
			return err
		}
		var geo netmap.Locator
		if locator, err := netmap.OpenGeoIP(*geoip); err == nil {
			defer locator.Close()
			geo = locator
		}
		route, err := model.Route(from, to, geo)
		if err != nil {
			return err
		}
		printRoute(route)
		return nil
	case "serve":
		return serveNetmap(ctx, *data, *geoip, places.Collectors, *out, *listen, *me)
	}
	fmt.Print(netmapUsage)
	return fmt.Errorf("unknown netmap command %q", command)
}

// serveNetmap is the map without the rest of the API, for working on the page.
func serveNetmap(ctx context.Context, data, geoip string, collectors []string, out, listen, me string) error {
	model, err := loadModel(data, geoip, collectors)
	if err != nil {
		return err
	}
	if err := writeOverview(model, out); err != nil {
		return err
	}
	var geo netmap.Locator
	if locator, err := netmap.OpenGeoIP(geoip); err == nil {
		defer locator.Close()
		geo = locator
	}
	pretend := net.ParseIP(me)
	service := netmap.NewService(netmap.ServiceOptions{Geo: geo, ClientIP: func(context.Context) net.IP { return pretend }})
	service.Use(model)
	mux := http.NewServeMux()
	service.Register(mux)
	mux.Handle("GET /netmap/data/", http.StripPrefix("/netmap/data/", http.FileServer(http.Dir(out))))
	server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	fmt.Printf("serving the map on http://%s/api/net/ and /netmap/data/\n", listen)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadModel reads the data directory and builds the map.
func loadModel(dir, geoip string, collectors []string) (*netmap.Model, error) {
	started := time.Now()
	sources, err := netmap.LoadSources(dir, collectors)
	if err != nil {
		return nil, err
	}
	for _, line := range sources.Report {
		fmt.Println(line)
	}
	if locator, err := netmap.OpenGeoIP(geoip); err == nil {
		defer locator.Close()
		sources.Geo = locator
	} else {
		fmt.Printf("geoip: %v — networks are placed by their data centres only\n", err)
	}
	model := netmap.Build(sources.Sources)
	fmt.Printf("map: %d networks, %d links, %d exchange points (%s)\n", len(model.Nodes), model.Links(), len(model.IXs), time.Since(started).Round(time.Millisecond))
	return model, nil
}

// writeOverview writes overview-<hash>.bin and points overview.json at it; the file of the day
// before and the one before that stay for pages that loaded the old manifest.
func writeOverview(model *netmap.Model, dir string) error {
	var buf bytes.Buffer
	if err := model.WriteOverview(&buf, netmap.DefaultOverview); err != nil {
		return err
	}
	sum := sha256.Sum256(buf.Bytes())
	name := "overview-" + hex.EncodeToString(sum[:6]) + ".bin"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, name), buf.Bytes()); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"file": name, "version": netmap.OverviewVersion, "built": model.Built.Format(time.RFC3339),
		"networks": len(model.Nodes), "links": model.Links(), "exchanges": len(model.IXs), "bytes": buf.Len(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "overview.json"), append(manifest, '\n')); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "overview-*.bin"))
	sort.Slice(old, func(i, j int) bool {
		a, _ := os.Stat(old[i])
		b, _ := os.Stat(old[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	for i, file := range old {
		if i >= 3 && filepath.Base(file) != name {
			_ = os.Remove(file)
		}
	}
	fmt.Printf("overview: %s, %d KB\n", filepath.Join(dir, name), buf.Len()/1024)
	return nil
}

func writeFileAtomic(path string, content []byte) error {
	temporary := path + ".new"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func printRoute(route *netmap.Route) {
	fmt.Printf("%s (AS%d) → %s (AS%d), about %.0f ms there and back\n", route.From.Addr, route.From.ASN, route.To.Addr, route.To.ASN, route.RTT)
	for i, hop := range route.Hops {
		fmt.Printf("%2d  AS%-10d %-40s %s\n", i+1, hop.ASN, hop.Name, hop.Country)
		if meeting := hop.Meeting; meeting != nil {
			where := strings.TrimSpace(strings.Join([]string{meeting.Name, meeting.City, meeting.Country}, " "))
			if where == "" {
				where = fmt.Sprintf("%.1f, %.1f (a guess)", meeting.Lat, meeting.Lon)
			}
			speeds := ""
			if meeting.Speeds[0] > 0 || meeting.Speeds[1] > 0 {
				speeds = fmt.Sprintf(", ports %s ↔ %s", speed(meeting.Speeds[0]), speed(meeting.Speeds[1]))
			}
			fmt.Printf("      ↓ %s, ~%.0f ms%s\n", where, meeting.RTT, speeds)
		}
	}
}

func speed(mbps int64) string {
	switch {
	case mbps <= 0:
		return "?"
	case mbps >= 1000:
		return fmt.Sprintf("%gG", float64(mbps)/1000)
	}
	return fmt.Sprintf("%dM", mbps)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitList(text string) []string {
	var out []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
