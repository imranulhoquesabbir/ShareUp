package main

import (
	"net"
	"strings"
	"time"
)

// Tiny mDNS responder: answers "shareup.local" with this PC's LAN address so phones that
// support mDNS can reach the PC by name. Best effort: any failure is ignored.

func mdnsReply(q []byte, ip4 net.IP) []byte {
	if len(q) < 12 || q[2]&0x80 != 0 || ip4 == nil {
		return nil
	}
	qd := int(q[4])<<8 | int(q[5])
	pos := 12
	for i := 0; i < qd; i++ {
		var labels []string
		for {
			if pos >= len(q) {
				return nil
			}
			l := int(q[pos])
			pos++
			if l == 0 {
				break
			}
			if l&0xC0 != 0 || pos+l > len(q) {
				return nil
			}
			labels = append(labels, strings.ToLower(string(q[pos:pos+l])))
			pos += l
		}
		if pos+4 > len(q) {
			return nil
		}
		qt := int(q[pos])<<8 | int(q[pos+1])
		pos += 4
		if strings.Join(labels, ".") == "shareup.local" && (qt == 1 || qt == 255) {
			r := []byte{0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}
			r = append(r, 7)
			r = append(r, "shareup"...)
			r = append(r, 5)
			r = append(r, "local"...)
			r = append(r, 0)
			r = append(r, 0, 1, 0x80, 1, 0, 0, 0, 120, 0, 4)
			r = append(r, ip4...)
			return r
		}
	}
	return nil
}

func mdnsLoop() {
	defer func() { recover() }()
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		return
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		ip := net.ParseIP(pickIP(get(), localIPs()))
		if ip == nil {
			continue
		}
		if resp := mdnsReply(buf[:n], ip.To4()); resp != nil {
			conn.WriteToUDP(resp, group)
		}
	}
}
