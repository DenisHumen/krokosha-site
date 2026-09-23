package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/netmap"
	"github.com/DenisHumen/krokosha-site/api/internal/trace"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

const netmapUsage = `Usage: krokosha-cli netmap <command> [flags]

The map of the internet of the /map page (docs/netmap.md).

  sync     the nightly job (krokosha-netmap.timer): fetch the sources, build the map, write what
           changed to MySQL and the overview the page downloads; the API loads the new map itself
  fetch    download the sources into the data directory: CAIDA AS Relationships (once a month),
           iptoasn, today's RouteViews snapshots, PeeringDB
  build    build the map from the data directory and write the overview, without MySQL
  route    print the likely path between two addresses: krokosha-cli netmap route FROM TO
  trace    measure the way from this server to an address, hop by hop: krokosha-cli netmap trace TO
           (UDP probes as tracepath sends them — no privileges needed — and TCP handshakes)
  serve    for development: answer /api/net/* and serve the overview from memory, without MySQL
           (the web dev server passes them on with NETMAP_DEV_API=http://127.0.0.1:8091)

Flags (before the addresses of route):
  --data DIR        the sources (default: NETMAP_DATA_DIR or /var/lib/krokosha/netmap)
  --out DIR         the overview (default: NETMAP_OUT_DIR or /var/www/krokosha/netmap)
  --geoip FILE      a MaxMind DB file with coordinates (default: GEOIP_DB)
  --collectors LIST RouteViews collectors (default: route-views2,route-views6)
  --env-file PATH   sync: settings of the installed site (default /etc/krokosha/env)
  --no-fetch        sync: use the sources already in the data directory (also NETMAP_FETCH=off)
  --force           sync: save the map even when it is much smaller than the stored one
`

// netmapUserAgent introduces the site to the sources.
const netmapUserAgent = "krokosha-site-netmap/1 (+https://github.com/DenisHumen/krokosha-site)"

// keepOverviews is how many overview files stay: the newest, and older ones for pages that loaded
// an older manifest.
const keepOverviews = 3

func runNetmap(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(netmapUsage)
		return nil
	}
	command, args := args[0], args[1:]
	flags := flag.NewFlagSet("netmap "+command, flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, netmapUsage) }
	data := flags.String("data", envOr("NETMAP_DATA_DIR", "/var/lib/krokosha/netmap"), "")
	geoip := flags.String("geoip", envOr("GEOIP_DB", "/var/lib/GeoIP/dbip-city-lite.mmdb"), "")
	collectors := flags.String("collectors", strings.Join(netmap.DefaultPlaces.Collectors, ","), "")
	out := flags.String("out", envOr("NETMAP_OUT_DIR", "/var/www/krokosha/netmap"), "")
	envFile := flags.String("env-file", config.DefaultEnvFile, "")
	noFetch := flags.Bool("no-fetch", false, "")
	force := flags.Bool("force", false, "")
	listen := flags.String("listen", "127.0.0.1:8091", "")
	me := flags.String("me", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	places := netmap.DefaultPlaces
	places.Collectors = splitList(*collectors)

	switch command {
	case "sync":
		// NETMAP_FETCH=off (install.sh --netmap-offline, tests): the files already there, never the network.
		offline, _ := config.LookupWithFile(*envFile)("NETMAP_FETCH")
		fetch := !*noFetch && !strings.EqualFold(strings.TrimSpace(offline), "off")
		return syncNetmap(ctx, syncOptions{envFile: *envFile, data: *data, out: *out, places: places, fetch: fetch, force: *force})
	case "fetch":
		return newFetcher(places, func(format string, args ...any) { fmt.Printf(format+"\n", args...) }).FetchAll(ctx, *data)
	case "build":
		model, err := loadModel(*data, *geoip, places.Collectors, printLine)
		if err != nil {
			return err
		}
		name, err := writeOverview(model, *out)
		if err != nil {
			return err
		}
		return publishOverview(model, *out, name)
	case "route":
		if flags.NArg() != 2 {
			return errors.New("usage: krokosha-cli netmap route [flags] FROM TO")
		}
		from, errFrom := netip.ParseAddr(flags.Arg(0))
		to, errTo := netip.ParseAddr(flags.Arg(1))
		if err := errors.Join(errFrom, errTo); err != nil {
			return err
		}
		model, err := loadModel(*data, *geoip, places.Collectors, printLine)
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
	case "trace":
		if flags.NArg() != 1 {
			return errors.New("usage: krokosha-cli netmap trace ADDRESS")
		}
		to, err := netip.ParseAddr(flags.Arg(0))
		if err != nil {
			return err
		}
		result, err := trace.Run(ctx, to, trace.Options{})
		if err != nil && result == nil {
			return err
		}
		printTrace(result)
		return err
	}
	fmt.Print(netmapUsage)
	return fmt.Errorf("unknown netmap command %q", command)
}

type syncOptions struct {
	envFile   string
	data, out string
	places    netmap.Places
	fetch     bool
	force     bool
}

// syncNetmap is the nightly job: fresh sources, the map built, only its changes written to
// MySQL, the overview for the page. The run is noted in netmap_sync, failed or not; the API loads
// the map of the newest successful one.
func syncNetmap(ctx context.Context, opt syncOptions) error {
	env, err := config.LoadEnv(config.LookupWithFile(opt.envFile))
	if err != nil {
		return fmt.Errorf("%w (is the site installed? try sudo, or --env-file)", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := db.Open(ctx, env.MySQL, 60*time.Second, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, env.MySQL, migrations.Files, log); err != nil {
		return err
	}
	unlock, err := lockSync(ctx, pool)
	if err != nil {
		return err
	}
	defer unlock()

	started := time.Now()
	id, err := netmap.StartSync(ctx, pool, started)
	if err != nil {
		return err
	}
	var report []string
	note := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Println(line)
		report = append(report, line)
	}
	finish := func(stats netmap.SaveStats, overview string, failure error) error {
		// The row must say how the run ended even when it was stopped.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return errors.Join(failure, netmap.FinishSync(finishCtx, pool, id, time.Now(), stats, overview, strings.Join(report, "\n"), failure))
	}

	if opt.fetch {
		// What cannot be fetched today is still there from an earlier day: the map is built from it.
		if err := newFetcher(opt.places, note).FetchAll(ctx, opt.data); err != nil {
			note("fetch: %v", err)
		}
		if ctx.Err() != nil {
			return finish(netmap.SaveStats{}, "", ctx.Err())
		}
	}
	model, err := loadModel(opt.data, env.GeoIPDB, opt.places.Collectors, note)
	if err != nil {
		return finish(netmap.SaveStats{}, "", err)
	}
	runtime.GC() // the sources are read: their memory goes before the tables are compared

	// The overview goes out first, under a name of its own; the manifest that points the page at it
	// changes only once the tables hold the same map.
	name, err := writeOverview(model, opt.out)
	if err != nil {
		return finish(netmap.SaveStats{}, "", err)
	}
	saved := time.Now()
	stats, err := netmap.Save(ctx, pool, model, netmap.SaveOptions{Sync: id, Day: started, Force: opt.force})
	if err != nil {
		return finish(stats, "", err)
	}
	if stats.First {
		note("saved the first map (%s)", time.Since(saved).Round(time.Second))
	} else {
		note("saved: links +%d −%d, %d changed kind; networks +%d −%d (%s)", stats.Added, stats.Gone, stats.Changed,
			stats.NetworksAdded, stats.NetworksGone, time.Since(saved).Round(time.Second))
	}
	if err := publishOverview(model, opt.out, name); err != nil {
		return finish(stats, "", err)
	}
	if err := netmap.Forget(ctx, pool, started); err != nil {
		note("forgetting what went away long ago: %v", err)
	}
	note("done in %s", time.Since(started).Round(time.Second))
	return finish(stats, name, nil)
}

// lockSync makes sure one sync runs at a time: a named lock of MySQL, held by a connection of its
// own until unlock.
func lockSync(ctx context.Context, pool *sql.DB) (func(), error) {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('krokosha_netmap_sync', 0)`).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if got.Int64 != 1 {
		_ = conn.Close()
		return nil, errors.New("another sync of the map is running")
	}
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT RELEASE_LOCK('krokosha_netmap_sync')`)
		_ = conn.Close()
	}, nil
}

func newFetcher(places netmap.Places, log func(format string, args ...any)) *netmap.Fetcher {
	return &netmap.Fetcher{
		Places: places, Client: &http.Client{Timeout: 20 * time.Minute}, UserAgent: netmapUserAgent,
		PDBKey: os.Getenv("PEERINGDB_API_KEY"), PDBPause: netmap.PeeringDBPause, Log: log,
	}
}

func printLine(format string, args ...any) { fmt.Printf(format+"\n", args...) }

// serveNetmap is the map without the rest of the API, for working on the page.
func serveNetmap(ctx context.Context, data, geoip string, collectors []string, out, listen, me string) error {
	model, err := loadModel(data, geoip, collectors, printLine)
	if err != nil {
		return err
	}
	name, err := writeOverview(model, out)
	if err != nil {
		return err
	}
	if err := publishOverview(model, out, name); err != nil {
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

// loadModel reads the data directory and builds the map; a GeoIP file that cannot be opened
// leaves networks placed by their data centres and neighbours only.
func loadModel(dir, geoip string, collectors []string, note func(format string, args ...any)) (*netmap.Model, error) {
	started := time.Now()
	sources, err := netmap.LoadSources(dir, collectors)
	if err != nil {
		return nil, err
	}
	for _, line := range sources.Report {
		note("%s", line)
	}
	if geoip == "" {
		note("geoip: off — networks are placed by their data centres and neighbours only")
	} else if locator, err := netmap.OpenGeoIP(geoip); err == nil {
		defer locator.Close()
		sources.Geo = locator
	} else {
		note("geoip: %v — networks are placed by their data centres and neighbours only", err)
	}
	model := netmap.Build(sources.Sources)
	note("map: %d networks, %d links, %d exchange points (%s)", len(model.Nodes), model.Links(), len(model.IXs), time.Since(started).Round(time.Millisecond))
	return model, nil
}

// writeOverview writes overview-<hash>.bin — and a gzipped copy nginx sends as it is — without
// pointing the page at it yet.
func writeOverview(model *netmap.Model, dir string) (string, error) {
	var buf bytes.Buffer
	if err := model.WriteOverview(&buf, netmap.DefaultOverview); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	name := "overview-" + hex.EncodeToString(sum[:6]) + ".bin"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var packed bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&packed, gzip.BestCompression)
	_, _ = zw.Write(buf.Bytes())
	if err := zw.Close(); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(dir, name), buf.Bytes()); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(dir, name+".gz"), packed.Bytes()); err != nil {
		return "", err
	}
	fmt.Printf("overview: %s, %d KB (%d KB gzipped)\n", filepath.Join(dir, name), buf.Len()/1024, packed.Len()/1024)
	return name, nil
}

// publishOverview points overview.json at the file; the older files stay a while for pages that
// loaded the old manifest.
func publishOverview(model *netmap.Model, dir, name string) error {
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"file": name, "version": netmap.OverviewVersion, "built": model.Built.Format(time.RFC3339),
		"networks": len(model.Nodes), "links": model.Links(), "exchanges": len(model.IXs), "bytes": info.Size(),
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
		if i >= keepOverviews && filepath.Base(file) != name {
			_ = os.Remove(file)
			_ = os.Remove(file + ".gz")
		}
	}
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

func printTrace(result *trace.Result) {
	fmt.Printf("%s → %s, %s\n", result.From, result.To, result.Took.Round(time.Millisecond))
	for _, hop := range result.Hops {
		who := "*"
		if hop.Addr.IsValid() {
			who = hop.Addr.String()
			for _, other := range hop.Others {
				who += " " + other.String()
			}
		}
		times := make([]string, len(hop.RTTs))
		for i, rtt := range hop.RTTs {
			times[i] = fmt.Sprintf("%.1f ms", float64(rtt.Microseconds())/1000)
		}
		note := ""
		switch {
		case hop.Reached:
			note = "  ← the address itself"
		case hop.Refused != "":
			note = "  ← " + hop.Refused
		}
		fmt.Printf("%2d  %-40s %d/%d  %s%s\n", hop.TTL, who, len(hop.RTTs), hop.Sent, strings.Join(times, "  "), note)
	}
	if c := result.Connect; c != nil {
		times := make([]string, len(c.RTTs))
		for i, rtt := range c.RTTs {
			times[i] = fmt.Sprintf("%.1f ms", float64(rtt.Microseconds())/1000)
		}
		fmt.Printf("TCP %d: %d/%d  %s\n", c.Port, len(c.RTTs), c.Sent, strings.Join(times, "  "))
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
