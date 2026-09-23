-- The addresses of the ports of networks at exchange points (PeeringDB): a hop of a traceroute
-- that answers from a peering LAN is the router of the network whose port has that address
-- (docs/netmap.md). Rebuilt every night with the rest of the map, and kept out of backups the same way.
CREATE TABLE IF NOT EXISTS netmap_ixaddr (
    addr VARBINARY(16) NOT NULL PRIMARY KEY,           -- 4 bytes of IPv4, 16 of IPv6
    asn  INT UNSIGNED  NOT NULL,
    ix   INT UNSIGNED  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
