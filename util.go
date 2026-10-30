package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	appName    = "LocalDrop"
	appVersion = "0.1.0"
)

// codeAlphabet excludes easily confused characters (0/O, 1/l/I).
const codeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func randomCode(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(codeAlphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = codeAlphabet[v.Int64()]
	}
	return string(b)
}

func randomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}

// lanIPv4s returns non-loopback IPv4 addresses, private ranges first.
func lanIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return ipScore(out[i]) > ipScore(out[j]) })
	return out
}

func ipScore(s string) int {
	ip := net.ParseIP(s)
	if ip == nil {
		return 0
	}
	if ip.IsPrivate() {
		return 2
	}
	if ip.IsGlobalUnicast() {
		return 1
	}
	return 0
}

func primaryLANIP() string {
	ips := lanIPv4s()
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackHost reports whether a Host header names the local machine
// (127.0.0.1, localhost, ::1). A DNS-rebound attacker name does not.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// naturalLess compares strings so digit runs sort numerically ("a2" < "a10").
func naturalLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(la) && j < len(lb) {
		ca, cb := la[i], lb[j]
		if isASCIIDigit(ca) && isASCIIDigit(cb) {
			si, sj := i, j
			for i < len(la) && isASCIIDigit(la[i]) {
				i++
			}
			for j < len(lb) && isASCIIDigit(lb[j]) {
				j++
			}
			na := strings.TrimLeft(la[si:i], "0")
			nb := strings.TrimLeft(lb[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(la)-i < len(lb)-j
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// contentDisposition builds an RFC 6266 header value with an ASCII fallback
// and an RFC 5987 filename* for UTF-8 names.
func contentDisposition(filename string) string {
	name := sanitizeFilename(filename)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", asciiFallback(name), rfc5987Encode(name))
}

// sanitizeFilename strips path separators and control characters from a name.
func sanitizeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "_")
	}
	if name == "" || name == "." || name == ".." {
		name = "download"
	}
	return name
}

func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' && r != ';' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), " _")
	if s == "" {
		s = "download"
	}
	return s
}

// rfc5987Encode percent-encodes a UTF-8 string for use in filename*.
func rfc5987Encode(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', strings.IndexByte(attrChars, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
