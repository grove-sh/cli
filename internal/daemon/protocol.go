package daemon

const (
	OpAcquire = "acquire"
	OpList    = "list"
)

type Request struct {
	Op       string  `json:"op"`
	Slug     string  `json:"slug,omitempty"`
	Worktree string  `json:"worktree,omitempty"`
	Entries  []Entry `json:"entries,omitempty"`
}

// An Entry is one thing to allocate. Routed entries get a hostname built from
// Label; the rest are ports with no name in DNS.
type Entry struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Routed   bool   `json:"routed,omitempty"`
	Detached bool   `json:"detached,omitempty"`
}

type Response struct {
	Error  string           `json:"error,omitempty"`
	Grants map[string]Grant `json:"grants,omitempty"`
	Leases []Live           `json:"leases,omitempty"`
}

// A Grant is a leased port, and the hostname routed to it when there is one. An
// attached grant lives only as long as the connection that asked for it.
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
}
