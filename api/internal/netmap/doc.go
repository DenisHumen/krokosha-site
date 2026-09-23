// Package netmap is the map of the internet on the site's /map page: which autonomous systems
// (AS) there are, which of them are linked and how (a customer and its provider, or two peers),
// where they meet (exchange points and data centres) and how fast their ports there are. From it a
// path between any two addresses is drawn, hop by hop, with the delay it is likely to take.
//
// Everything comes from open data, fetched once a day (docs/netmap.md):
//
//   - CAIDA AS Relationships — every link between two ASes with its kind, once a month;
//   - RouteViews — snapshots of the routing tables of collectors that many networks talk BGP to:
//     links that appeared or went away since yesterday;
//   - iptoasn — which AS announces an address, with the name and the country of the AS;
//   - PeeringDB — exchange points, data centres with their coordinates, who is present where and
//     with what port speed;
//   - the GeoIP database of the site (DB-IP City Lite) — where an address is.
//
// Nothing here is measured: a path is what the routing policies of the networks make likely.
// Measured paths (traceroute from the server, RIPE Atlas) come later, drawn on the same map.
package netmap
