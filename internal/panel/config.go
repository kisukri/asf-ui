package panel

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

type Settings struct {
	ListenAddr            string
	DatabasePath          string
	CredentialKeys        string
	CookieSecure          bool
	PublicOrigin          string
	TrustProxyHeaders     bool
	SessionIdleMinutes    int
	SessionAbsoluteHours  int
	AllowedPorts          [][2]int
	DeniedPorts           map[int]bool
	AllowPrivateCIDRs     []netip.Prefix
	RequireHTTPS          bool
	TimeoutSeconds        int
	MaxResponseBytes      int64
	MaxConnectionsPerUser int
	AllowUserConnections  bool
	SchedulerInterval     int
	SchedulerReassert     int
	SteamNameLookup       bool
	LogLevel              string
}

var cfg *Settings

const defaultDeniedPorts = "1433,1521,2375,2376,2379,2380,3306,5432,5984,6379,6443,8500,9200,9300,10250,11211,27017"

func loadDotenv() {
	path := os.Getenv("ENV_FILE")
	if path == "" {
		path = ".env"
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func loadSettings() (*Settings, error) {
	loadDotenv()
	var errs []string
	boolEnv := func(name string, def bool) bool {
		v := env(name, "")
		if v == "" {
			return def
		}
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}
	intEnv := func(name string, def, lo, hi int) int {
		v := env(name, "")
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Sprintf("%s must be an integer between %d and %d", name, lo, hi))
			return def
		}
		return n
	}
	s := &Settings{
		ListenAddr:            env("LISTEN_ADDR", "127.0.0.1:8000"),
		DatabasePath:          env("DATABASE_PATH", "data/panel.db"),
		CredentialKeys:        env("CREDENTIAL_KEYS", ""),
		CookieSecure:          boolEnv("COOKIE_SECURE", true),
		PublicOrigin:          strings.TrimRight(env("PUBLIC_ORIGIN", ""), "/"),
		TrustProxyHeaders:     boolEnv("TRUST_PROXY_HEADERS", false),
		SessionIdleMinutes:    intEnv("SESSION_IDLE_MINUTES", 720, 5, 60*24*7),
		SessionAbsoluteHours:  intEnv("SESSION_ABSOLUTE_HOURS", 24*7, 1, 24*90),
		RequireHTTPS:          boolEnv("ASF_REQUIRE_HTTPS", true),
		TimeoutSeconds:        intEnv("ASF_TIMEOUT_SECONDS", 8, 2, 30),
		MaxResponseBytes:      int64(intEnv("ASF_MAX_RESPONSE_BYTES", 4*1024*1024, 64*1024, 32*1024*1024)),
		MaxConnectionsPerUser: intEnv("ASF_MAX_CONNECTIONS_PER_USER", 5, 1, 50),
		AllowUserConnections:  boolEnv("ALLOW_USER_CONNECTIONS", false),
		SchedulerInterval:     intEnv("SCHEDULER_INTERVAL_SECONDS", 30, 10, 300),
		SchedulerReassert:     intEnv("SCHEDULER_REASSERT_SECONDS", 300, 60, 3600),
		SteamNameLookup:       boolEnv("STEAM_NAME_LOOKUP", true),
		LogLevel:              env("LOG_LEVEL", "INFO"),
		DeniedPorts:           map[int]bool{},
	}
	for _, part := range strings.Split(env("ASF_ALLOWED_PORTS", "80,443,1024-65535"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a, b, isRange := strings.Cut(part, "-")
		if !isRange {
			b = a
		}
		lo, e1 := strconv.Atoi(strings.TrimSpace(a))
		hi, e2 := strconv.Atoi(strings.TrimSpace(b))
		if e1 != nil || e2 != nil || lo < 1 || lo > hi || hi > 65535 {
			errs = append(errs, "invalid port range in ASF_ALLOWED_PORTS: "+part)
			continue
		}
		s.AllowedPorts = append(s.AllowedPorts, [2]int{lo, hi})
	}
	for _, p := range strings.Split(env("ASF_DENIED_PORTS", defaultDeniedPorts), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			s.DeniedPorts[n] = true
		}
	}
	for _, c := range strings.Split(env("ASF_ALLOW_PRIVATE_CIDRS", ""), ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			errs = append(errs, "invalid CIDR in ASF_ALLOW_PRIVATE_CIDRS: "+c)
			continue
		}
		s.AllowPrivateCIDRs = append(s.AllowPrivateCIDRs, p.Masked())
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return s, nil
}
