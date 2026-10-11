// Command winrelay runs on the Windows host and lets WSL reach the lab VMs.
// In WSL's mirrored networking mode, WSL doesn't see Hyper-V's internal
// switches, so it can't route to the VMs; Windows can. winrelay is a SOCKS5
// server (CONNECT only, no auth) on Windows' loopback, which WSL shares.
//
// It only connects to addresses inside the allowed prefixes (the lab
// switch's subnet), prints its listening address on stdout, and exits when
// its stdin closes, so it never outlives the test process that started it.
//
//	winrelay.exe -listen 127.0.0.1:0 -allow 172.31.192.0/20
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to listen on")
	allow := flag.String("allow", "", "comma-separated prefixes it may connect to")
	flag.Parse()
	var prefixes []netip.Prefix
	for _, p := range strings.Split(*allow, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			log.Fatalf("winrelay: -allow %q: %v", p, err)
		}
		prefixes = append(prefixes, pfx)
	}
	if len(prefixes) == 0 {
		log.Fatal("winrelay: -allow is required")
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("winrelay: %v", err)
	}
	fmt.Println(ln.Addr().String())
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalf("winrelay: accept: %v", err)
		}
		go serve(c, prefixes)
	}
}

func serve(c net.Conn, allowed []netip.Prefix) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	r := bufio.NewReader(c)
	target, err := handshake(r, c)
	if err != nil {
		return
	}
	ap, err := netip.ParseAddrPort(target)
	if err != nil || !inside(ap.Addr(), allowed) {
		_, _ = c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0}) // not allowed by ruleset
		return
	}
	up, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}) // connection refused
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, r); closeWrite(up); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

// handshake reads a SOCKS5 greeting and CONNECT request and returns the
// target address. Names aren't resolved: a name must be an IP address
// written out, which is how Chromium sends one.
func handshake(r *bufio.Reader, w io.Writer) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || hdr[0] != 5 {
		return "", errors.New("not SOCKS5")
	}
	if _, err := io.ReadFull(r, make([]byte, hdr[1])); err != nil {
		return "", err
	}
	if _, err := w.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	var req [4]byte
	if _, err := io.ReadFull(r, req[:]); err != nil || req[0] != 5 || req[1] != 1 {
		return "", errors.New("only CONNECT")
	}
	var ip net.IP
	switch req[3] {
	case 1:
		ip = make(net.IP, 4)
	case 4:
		ip = make(net.IP, 16)
	case 3:
		n, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		name := make([]byte, n)
		if _, err := io.ReadFull(r, name); err != nil {
			return "", err
		}
		if ip = net.ParseIP(string(name)); ip == nil {
			return "", errors.New("only IP addresses")
		}
	default:
		return "", errors.New("unknown address type")
	}
	if req[3] != 3 {
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", err
		}
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), fmt.Sprint(binary.BigEndian.Uint16(port[:]))), nil
}

func inside(a netip.Addr, allowed []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range allowed {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func closeWrite(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.CloseWrite()
	}
}
