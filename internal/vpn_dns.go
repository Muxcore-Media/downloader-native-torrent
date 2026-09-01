package internal

import (
	"os"
	"strings"
)

func writeResolvConf(servers []string) error {
	if len(servers) == 0 {
		return nil
	}
	var b strings.Builder
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		b.WriteString("nameserver ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return nil
	}
	return os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644)
}
