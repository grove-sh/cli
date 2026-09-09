package daemon

// A grove binary is rebuilt far more often than its daemon is restarted, so a
// mismatch has to name itself rather than surface as a missing field.
const Version = 3

const (
	OpAcquire = "acquire"
	OpResolve = "resolve"
	OpList    = "list"
	OpRelease = "release"
	OpStatus  = "status"
	OpStop    = "stop"
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
type Entry struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Routed   bool   `json:"routed,omitempty"`
	Detached bool   `json:"detached,omitempty"`
}

type Response struct {
	Version  int              `json:"version"`
	Error    string           `json:"error,omitempty"`
	Grants   map[string]Grant `json:"grants,omitempty"`
	Leases   []Live           `json:"leases,omitempty"`
	Released []string         `json:"released,omitempty"`
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
