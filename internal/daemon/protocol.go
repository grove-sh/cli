package daemon

// A grove binary is rebuilt far more often than its daemon is restarted, so a
// mismatch has to name itself rather than surface as a missing field.
const Version = 5

const (
	OpAcquire = "acquire"
	OpResolve = "resolve"
	OpList    = "list"
	OpRelease = "release"
	OpStatus  = "status"
	OpStop    = "stop"
	OpRecords = "records"
	OpForget  = "forget"
)

type Request struct {
	Version  int     `json:"version"`
	PID      int     `json:"pid,omitempty"`
	Op       string  `json:"op"`
	Slug     string  `json:"slug,omitempty"`
	Worktree string  `json:"worktree,omitempty"`
	Entries  []Entry `json:"entries,omitempty"`

	// Empty releases every detached lease the context holds.
	Names []string `json:"names,omitempty"`
}

// Routed entries get a hostname built from Label; the rest have no name in DNS.
// Aliases are further labels reaching the same port, so one app answers on more
// than one hostname without leasing a port per name.
type Entry struct {
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`
	Routed   bool     `json:"routed,omitempty"`
	Detached bool     `json:"detached,omitempty"`
}

type Response struct {
	Version  int              `json:"version"`
	Error    string           `json:"error,omitempty"`
	Grants   map[string]Grant `json:"grants,omitempty"`
	Leases   []Live           `json:"leases,omitempty"`
	Released []string         `json:"released,omitempty"`
	Records  []Record         `json:"records,omitempty"`
	Status   *Status          `json:"status,omitempty"`
}

type Status struct {
	Version int `json:"version"`
	// The build version, which Version does not track: the wire shape sits
	// still across many releases. Empty from a daemon built before this field,
	// which says the same thing.
	Grove  string `json:"grove,omitempty"`
	PID    int    `json:"pid"`
	Listen string `json:"listen"`

	Leases int `json:"leases"`
}

// An attached grant lives only as long as the connection that asked for it.
type Grant struct {
	Port int    `json:"port"`
	Host string `json:"host,omitempty"`
	URL  string `json:"url,omitempty"`

	// The composed alias hostnames, in the order the entry named them. Host is
	// the one the entry means when a single hostname is wanted.
	Aliases []string `json:"aliases,omitempty"`
}

// What ports.json holds for one entry. Unlike a lease this outlives the daemon
// and the worktree both, which is the whole reason anything reports on it.
type Record struct {
	Slug     string `json:"slug"`
	Service  string `json:"service"`
	Port     int    `json:"port"`
	Worktree string `json:"worktree,omitempty"`
}

type Live struct {
	Slug     string `json:"slug"`
	Service  string `json:"service,omitempty"`
	Worktree string `json:"worktree"`
	Port     int    `json:"port"`
	Host     string `json:"host,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	PID      int    `json:"pid,omitempty"`
}
