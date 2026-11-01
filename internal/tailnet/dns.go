package tailnet

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
)

const (
	typeA    = 1
	typeAAAA = 28
	classIN  = 1

	rcodeOK       = 0
	rcodeFormErr  = 1
	rcodeNXDomain = 3

	answerTTL = 30
)

// Refuse names outside the split DNS zone so peers cannot use this as a recursive resolver.
type Resolver struct {
	Zone string
	IPv4 netip.Addr
	IPv6 netip.Addr
}

func (r Resolver) Serve(conn net.PacketConn) error {
	buf := make([]byte, 512)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if reply := r.Answer(buf[:n]); reply != nil {
			conn.WriteTo(reply, addr)
		}
	}
}

func (r Resolver) Answer(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	flags := binary.BigEndian.Uint16(query[2:4])
	if flags&0x8000 != 0 {
		return nil
	}
	if binary.BigEndian.Uint16(query[4:6]) != 1 {
		return header(query, flags, rcodeFormErr, 0)
	}
	name, end, ok := readName(query, 12)
	if !ok || end+4 > len(query) {
		return header(query, flags, rcodeFormErr, 0)
	}
	qtype := binary.BigEndian.Uint16(query[end : end+2])
	question := query[12 : end+4]

	zone := strings.ToLower(strings.TrimSuffix(r.Zone, "."))
	if name != zone && !strings.HasSuffix(name, "."+zone) {
		return append(header(query, flags, rcodeNXDomain, 0), question...)
	}

	var addr netip.Addr
	switch qtype {
	case typeA:
		addr = r.IPv4
	case typeAAAA:
		addr = r.IPv6
	}
	// NXDOMAIN would also suppress valid records of other types for this name.
	if !addr.IsValid() {
		return append(header(query, flags, rcodeOK, 0), question...)
	}
	out := append(header(query, flags, rcodeOK, 1), question...)
	out = append(out, 0xc0, 0x0c)
	out = binary.BigEndian.AppendUint16(out, qtype)
	out = binary.BigEndian.AppendUint16(out, classIN)
	out = binary.BigEndian.AppendUint32(out, answerTTL)
	ip := addr.AsSlice()
	out = binary.BigEndian.AppendUint16(out, uint16(len(ip)))
	return append(out, ip...)
}

func header(query []byte, flags uint16, rcode uint16, answers uint16) []byte {
	out := make([]byte, 12)
	copy(out[0:2], query[0:2])
	binary.BigEndian.PutUint16(out[2:4], 0x8400|flags&0x0100|rcode)
	questions := uint16(1)
	if rcode == rcodeFormErr {
		questions = 0
	}
	binary.BigEndian.PutUint16(out[4:6], questions)
	binary.BigEndian.PutUint16(out[6:8], answers)
	return out
}

func readName(msg []byte, offset int) (string, int, bool) {
	var labels []string
	for {
		if offset >= len(msg) {
			return "", 0, false
		}
		length := int(msg[offset])
		if length == 0 {
			return strings.ToLower(strings.Join(labels, ".")), offset + 1, true
		}
		if length > 63 || offset+1+length > len(msg) {
			return "", 0, false
		}
		labels = append(labels, string(msg[offset+1:offset+1+length]))
		offset += 1 + length
	}
}
