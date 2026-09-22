#!/usr/bin/env python3
"""Writes a MaxMind DB small enough for a test: IPv4 networks with a country and a city.

    mmdb.py OUT.mmdb 127.0.0.0/8=UA:Kyiv 203.0.113.0/24=DE:

The format (https://maxmind.github.io/MaxMind-DB/): a binary search tree over the bits of an
address, sixteen zero bytes, the data the leaves point to, a marker, the metadata. The same
writer, in Go, is api/internal/geo/geotest.
"""
import ipaddress
import os
import sys


def control(kind, size):
    if kind > 7:  # extended type: the number goes into a byte of its own
        first = control(0, size)
        return first[:1] + bytes([kind - 7]) + first[1:]
    if size < 29:
        return bytes([kind << 5 | size])
    return bytes([kind << 5 | 29, size - 29])


def number(value):
    return value.to_bytes(8, 'big').lstrip(b'\0')


def encode(value):
    if isinstance(value, str):
        raw = value.encode('utf-8')
        return control(2, len(raw)) + raw
    if isinstance(value, tuple):  # (kind, integer): 5 uint16, 6 uint32, 9 uint64
        kind, integer = value
        return control(kind, len(number(integer))) + number(integer)
    if isinstance(value, list):
        return control(11, len(value)) + b''.join(encode(item) for item in value)
    if isinstance(value, dict):
        out = control(7, len(value))
        for key in sorted(value):
            out += encode(key) + encode(value[key])
        return out
    raise TypeError(type(value))


class Node:
    def __init__(self):
        self.children = [None, None]
        self.leaf = False
        self.offset = 0
        self.index = 0


def database(networks):
    root = Node()
    data = b''
    for text in sorted(networks):
        prefix = ipaddress.ip_network(text)
        address = int(prefix.network_address)
        node = root
        for bit in range(prefix.prefixlen):
            side = address >> (31 - bit) & 1
            if bit == prefix.prefixlen - 1:
                node.children[side] = Node()
                node.children[side].leaf = True
                node.children[side].offset = len(data)
                break
            if node.children[side] is None:
                node.children[side] = Node()
            node = node.children[side]
        data += encode(networks[text])

    inner = []
    queue = [root]
    while queue:
        node = queue.pop(0)
        node.index = len(inner)
        inner.append(node)
        queue.extend(child for child in node.children if child is not None and not child.leaf)
    out = bytearray()
    for node in inner:
        for child in node.children:
            value = len(inner)  # «nothing here»
            if child is not None and child.leaf:
                value = len(inner) + 16 + child.offset
            elif child is not None:
                value = child.index
            out += bytes([value >> 16 & 255, value >> 8 & 255, value & 255])
    out += bytes(16)
    out += data
    out += b'\xab\xcd\xefMaxMind.com'
    out += encode({
        'binary_format_major_version': (5, 2), 'binary_format_minor_version': (5, 0),
        'build_epoch': (9, 1789000000), 'database_type': 'Test-City', 'description': {'en': 'written by a test'},
        'ip_version': (5, 4), 'languages': ['en'], 'node_count': (6, len(inner)), 'record_size': (5, 24),
    })
    return bytes(out)


def place(country, city):
    record = {'country': {'iso_code': country, 'names': {'en': country}}}
    if city:
        record['city'] = {'names': {'en': city}}
    return record


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    path = sys.argv[1]
    networks = {}
    for item in sys.argv[2:]:
        network, where = item.split('=', 1)
        country, _, city = where.partition(':')
        networks[network] = place(country, city)
    temporary = path + '.new'
    with open(temporary, 'wb') as out:
        out.write(database(networks))
    os.chmod(temporary, 0o644)
    os.rename(temporary, path)  # the way geoipupdate replaces the file


if __name__ == '__main__':
    main()
