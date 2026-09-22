package lease_test

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/grove-sh/cli/internal/lease"
	"github.com/grove-sh/cli/internal/shell"
)

func registry(t *testing.T, opts lease.Options) *lease.Registry {
	t.Helper()
	r, err := lease.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func acquire(t *testing.T, r *lease.Registry, slug, service, worktree string) *lease.Lease {
	t.Helper()
	l, err := r.Acquire(lease.Request{Slug: slug, Service: service, Worktree: worktree})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}

// allFree replaces the bind probe so allocation tests do not depend on what the
// machine happens to be running.
func allFree(int) bool { return true }

func TestPortComesFromTheRange(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20099}
	r := registry(t, lease.Options{Range: rng, Free: allFree})

	l := acquire(t, r, "app1", "web", "/src/app1")

	if l.Port < rng.Low || l.Port > rng.High {
		t.Errorf("port %d is outside %s", l.Port, rng)
	}
}

// No hints file, no state on disk: the same context hashes to the same port on
// a fresh registry, which is where port stability comes from.
func TestSameContextGetsTheSamePortWithoutPersistence(t *testing.T) {
	opts := lease.Options{Free: allFree}
	first := acquire(t, registry(t, opts), "app1-feat1", "web", "/src/feat1")
	second := acquire(t, registry(t, opts), "app1-feat1", "web", "/src/feat1")

	if first.Port != second.Port {
		t.Errorf("ports %d and %d differ across registries", first.Port, second.Port)
	}
}

func TestServicesOfOneContextGetDifferentPorts(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})

	web := acquire(t, r, "app1", "web", "/src/app1")
	api := acquire(t, r, "app1", "api", "/src/app1")

	if web.Port == api.Port {
		t.Errorf("both services got port %d", web.Port)
	}
}

func TestOccupiedPortIsSkipped(t *testing.T) {
	r := registry(t, lease.Options{})

	wanted := acquire(t, r, "app1", "web", "/src/app1")
	port := wanted.Port
	wanted.Release()

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("could not occupy port %d: %v", port, err)
	}
	defer ln.Close()

	again := acquire(t, r, "app1", "web", "/src/app1")
	if again.Port == port {
		t.Errorf("handed out port %d while it was listening", port)
	}
}

func TestReleaseFreesThePort(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})

	first, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})
	if err != nil {
		t.Fatal(err)
	}
	port := first.Port
	first.Release()

	second := acquire(t, r, "app1", "web", "/src/app1")
	if second.Port != port {
		t.Errorf("port = %d after release, want %d back", second.Port, port)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	l, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})
	if err != nil {
		t.Fatal(err)
	}

	l.Release()
	l.Release()

	if got := len(r.List()); got != 0 {
		t.Errorf("%d leases remain", got)
	}
}

func TestSecondAcquireOfOneServiceIsBusy(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	first := acquire(t, r, "app1", "web", "/src/app1")

	_, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})

	var busy *lease.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want BusyError", err)
	}
	if busy.Port != first.Port {
		t.Errorf("BusyError names port %d, want %d", busy.Port, first.Port)
	}
}

// Two worktrees that resolve to one slug must not both claim the hostname.
// identity.Resolve cannot see this, so the registry is where it gets caught.
func TestSameSlugFromAnotherWorktreeCollides(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	acquire(t, r, "app1-feat1", "web", "/src/feat.1")

	_, err := r.Acquire(lease.Request{Slug: "app1-feat1", Service: "web", Worktree: "/src/feat-1"})

	var collision *lease.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want CollisionError", err)
	}
	if collision.Held != "/src/feat.1" || collision.Wanted != "/src/feat-1" {
		t.Errorf("collision names %q and %q", collision.Held, collision.Wanted)
	}
}

func TestCollisionClearsWhenTheHolderReleases(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	held, err := r.Acquire(lease.Request{Slug: "app1-feat1", Service: "web", Worktree: "/src/feat.1"})
	if err != nil {
		t.Fatal(err)
	}

	held.Release()

	if _, err := r.Acquire(lease.Request{Slug: "app1-feat1", Service: "web", Worktree: "/src/feat-1"}); err != nil {
		t.Errorf("still colliding after release: %v", err)
	}
}

func TestOwnershipSurvivesWhileAnotherServiceHoldsIt(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	web, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})
	if err != nil {
		t.Fatal(err)
	}
	acquire(t, r, "app1", "api", "/src/app1")

	web.Release()

	var collision *lease.CollisionError
	if _, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/elsewhere/app1"}); !errors.As(err, &collision) {
		t.Errorf("err = %v, want CollisionError while api still holds the context", err)
	}
}

func TestExhaustedRange(t *testing.T) {
	r := registry(t, lease.Options{Range: lease.PortRange{Low: 20000, High: 20000}, Free: allFree})
	acquire(t, r, "app1", "web", "/src/app1")

	_, err := r.Acquire(lease.Request{Slug: "app2", Service: "web", Worktree: "/src/app2"})

	var exhausted *lease.ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want ExhaustedError", err)
	}
}

func TestConcurrentAcquiresNeverShareAPort(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})

	const n = 50
	var wg sync.WaitGroup
	ports := make([]int, n)
	for i := range n {
		wg.Go(func() {
			slug := "app" + strconv.Itoa(i)
			l, err := r.Acquire(lease.Request{Slug: slug, Service: "web", Worktree: "/src/" + slug})
			if err != nil {
				t.Error(err)
				return
			}
			ports[i] = l.Port
		})
	}
	wg.Wait()

	seen := make(map[int]bool, n)
	for i, port := range ports {
		if port == 0 {
			t.Fatalf("goroutine %d got no port", i)
		}
		if seen[port] {
			t.Fatalf("port %d handed out twice", port)
		}
		seen[port] = true
	}
	if got := len(r.List()); got != n {
		t.Errorf("List reports %d leases, want %d", got, n)
	}
}

func TestListIsSorted(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	acquire(t, r, "app2", "web", "/src/app2")
	acquire(t, r, "app1", "web", "/src/app1")
	acquire(t, r, "app1", "api", "/src/app1")

	got := r.List()

	want := [][2]string{{"app1", "api"}, {"app1", "web"}, {"app2", "web"}}
	if len(got) != len(want) {
		t.Fatalf("got %d leases, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Slug != w[0] || got[i].Service != w[1] {
			t.Errorf("[%d] = %s/%s, want %s/%s", i, got[i].Slug, got[i].Service, w[0], w[1])
		}
	}
}

// A detached lease is re-asserted by every command in the context, so acquiring
// one twice hands back the same allocation instead of reporting a clash.
func TestDetachedAcquireIsIdempotent(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	req := lease.Request{Slug: "app1", Service: "studio", Worktree: "/src/app1", Detached: true}

	first, err := r.Acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Acquire(req)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	if first.Port != second.Port {
		t.Errorf("ports %d and %d differ", first.Port, second.Port)
	}
	if got := len(r.List()); got != 1 {
		t.Errorf("%d leases, want 1", got)
	}
}

// Something else binds a detached port, so a port already in use is expected.
// Walking past it would hand back a number nothing is listening on.
func TestDetachedAllocationDoesNotProbe(t *testing.T) {
	r := registry(t, lease.Options{})
	req := lease.Request{Slug: "app1", Service: "studio", Worktree: "/src/app1", Detached: true}

	wanted, err := r.Acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	port := wanted.Port
	wanted.Release()

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("could not occupy port %d: %v", port, err)
	}
	defer ln.Close()

	again, err := r.Acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Port != port {
		t.Errorf("port = %d, want the same %d the stack is already on", again.Port, port)
	}
}

func TestAttachedAndDetachedCannotShareAKey(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	if _, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"}); err != nil {
		t.Fatal(err)
	}

	_, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1", Detached: true})

	var busy *lease.BusyError
	if !errors.As(err, &busy) {
		t.Errorf("err = %v, want BusyError", err)
	}
}

// Predicting a port is only possible because allocation is arithmetic, and it
// has to agree with what Acquire actually hands out.
func TestPredictPortMatchesWhatAcquireGives(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20999}
	r := registry(t, lease.Options{Range: rng, Free: allFree})

	held := acquire(t, r, "app1-feat1", "web", "/src/feat1")

	if got := lease.PredictPort(rng, "app1-feat1", "web"); got != held.Port {
		t.Errorf("PredictPort = %d, Acquire gave %d", got, held.Port)
	}
}

func TestPredictPortUsesTheDefaultRangeWhenGivenNone(t *testing.T) {
	port := lease.PredictPort(lease.PortRange{}, "app1", "web")

	if port < lease.DefaultRange.Low || port > lease.DefaultRange.High {
		t.Errorf("port = %d, outside %s", port, lease.DefaultRange)
	}
}

// A clash names the process holding the lease, because the port cannot: a
// command that hangs mid shutdown keeps its lease after it stops listening,
// which is exactly when this error appears.
func TestBusyErrorNamesTheHolder(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	if _, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1", PID: 4242}); err != nil {
		t.Fatal(err)
	}

	_, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1", PID: 99})

	var busy *lease.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want BusyError", err)
	}
	if busy.PID != 4242 {
		t.Errorf("BusyError names pid %d, want the holder 4242", busy.PID)
	}
	if !strings.Contains(err.Error(), "pid 4242") {
		t.Errorf("message does not name the holder: %v", err)
	}
}

func TestBusyErrorWithoutAPIDStaysReadable(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	if _, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"}); err != nil {
		t.Fatal(err)
	}

	_, err := r.Acquire(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})

	if strings.Contains(err.Error(), "pid") {
		t.Errorf("message invents a holder: %v", err)
	}
}

// A detached lease has no process holding it: the command that asserted it has
// exited by definition, so a clash points at the way to end it instead.
func TestBusyErrorForADetachedLeasePointsAtRelease(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	if _, err := r.Acquire(lease.Request{Slug: "app1", Service: "studio", Worktree: "/src/app1", Detached: true, PID: 4242}); err != nil {
		t.Fatal(err)
	}

	_, err := r.Acquire(lease.Request{Slug: "app1", Service: "studio", Worktree: "/src/app1", PID: 99})

	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "pid") {
		t.Errorf("named a process that has exited: %v", err)
	}
	if !strings.Contains(err.Error(), "grove release studio") {
		t.Errorf("does not say how to end it: %v", err)
	}
}

// Two entries whose hashes collide is ordinary at a few dozen worktrees, and
// used to fail the second one with "no free port" while hundreds were free.
func TestADetachedCollisionWalksInsteadOfFailing(t *testing.T) {
	// A range of two ports makes the collision certain rather than lucky.
	rng := lease.PortRange{Low: 20000, High: 20001}
	r := registry(t, lease.Options{Range: rng, Free: allFree, Memory: &recorder{}})

	first, err := r.Acquire(lease.Request{Slug: "app1", Service: "db", Worktree: "/src/app1", Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})
	if err != nil {
		t.Fatalf("a colliding detached lease still fails: %v", err)
	}

	if first.Port == second.Port {
		t.Errorf("both landed on %d", first.Port)
	}
	for _, l := range []*lease.Lease{first, second} {
		if !rng.Holds(l.Port) {
			t.Errorf("port %d is outside %s", l.Port, rng)
		}
	}
}

// The walk is only safe because it is written down. A daemon that starts again
// has to hand the same entry the same port, whichever context asks first, or it
// points one stack at another's containers.
func TestARememberedPortSurvivesTheDaemon(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20001}
	shared := &recorder{}

	before := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})
	if _, err := before.Acquire(lease.Request{Slug: "app1", Service: "db", Worktree: "/src/app1", Detached: true}); err != nil {
		t.Fatal(err)
	}
	walked, err := before.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})
	if err != nil {
		t.Fatal(err)
	}

	// Nothing survives a restart but the record, and this time the entry that
	// walked asks first, with its hashed port free for the taking.
	after := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})
	again, err := after.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})
	if err != nil {
		t.Fatal(err)
	}

	if again.Port != walked.Port {
		t.Errorf("port %d became %d across a restart, so a stack is now published somewhere grove is not looking", walked.Port, again.Port)
	}
}

// A remembered port another entry has taken since is no better than no record,
// and walking again has to update it rather than leave the old answer.
func TestARememberedPortGivesWayAndIsWrittenAgain(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20001}
	shared := &recorder{ports: map[string]int{"app2\x00db": 20000}}

	r := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})
	first := acquireDetached(t, r, "app1", "db", "/src/app1")
	second, err := r.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})
	if err != nil {
		t.Fatal(err)
	}

	if second.Port == first.Port {
		t.Fatalf("the record handed out a port already held: %d", second.Port)
	}
	if got := shared.ports["app2\x00db"]; got != second.Port {
		t.Errorf("the record still says %d, not the %d it walked to", got, second.Port)
	}
}

// A range with nothing left is the one case that really is exhausted, and it
// still has to say so rather than hand back a port someone holds.
func TestADetachedLeaseStillReportsAFullRange(t *testing.T) {
	r := registry(t, lease.Options{Range: lease.PortRange{Low: 20000, High: 20000}, Free: allFree, Memory: &recorder{}})
	acquireDetached(t, r, "app1", "db", "/src/app1")

	_, err := r.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})

	var exhausted *lease.ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want ExhaustedError", err)
	}
}

// Neither a lease nor a record is something ls will show, so the one error
// that can name no holder has to name what to do instead.
func TestExhaustedErrorNamesTheRemedy(t *testing.T) {
	r := registry(t, lease.Options{Range: lease.PortRange{Low: 20000, High: 20000}, Free: allFree, Memory: &recorder{}})
	acquireDetached(t, r, "app1", "db", "/src/app1")

	_, err := r.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})

	if err == nil {
		t.Fatal("a full range was not refused")
	}
	if !strings.Contains(err.Error(), shell.Invocation()+" release") {
		t.Errorf("err = %q, and names no command to run", err)
	}
}

func acquireDetached(t *testing.T, r *lease.Registry, slug, service, worktree string) *lease.Lease {
	t.Helper()
	l, err := r.Acquire(lease.Request{Slug: slug, Service: service, Worktree: worktree, Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}

// The entry that wins a collision has to be written down too, or the record
// defends only the loser.
func TestEveryDetachedAllocationIsRemembered(t *testing.T) {
	shared := &recorder{}
	r := registry(t, lease.Options{Free: allFree, Memory: shared})

	got := acquireDetached(t, r, "app1", "db", "/src/app1")

	if port, ok := shared.ports["app1\x00db"]; !ok || port != got.Port {
		t.Errorf("the record says %d, %v; want the %d the entry took without walking", port, ok, got.Port)
	}
}

// A stack whose daemon is gone leaves no lease behind, and a detached pick
// never probes, so the record is the only thing that can say the port is
// spoken for. A stopped stack and a deleted worktree look alike from here, so
// while there is anywhere else to go the record is honoured either way.
func TestAnotherEntrysRecordIsLeftAloneWhileTheRangeHasRoom(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20001}
	contested := lease.PredictPort(rng, "app2", "db")
	shared := &recorder{ports: map[string]int{"app1\x00db": contested}}

	r := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})
	got := acquireDetached(t, r, "app2", "db", "/src/app2")

	if got.Port == contested {
		t.Fatalf("took app1's recorded port %d with the rest of the range free", contested)
	}
	if port, ok := shared.ports["app1\x00db"]; !ok || port != contested {
		t.Errorf("app1's record is %d, %v; nothing should have disturbed it", port, ok)
	}
	if port := shared.ports["app2\x00db"]; port != got.Port {
		t.Errorf("the record says app2 is on %d, not the %d it walked to", port, got.Port)
	}
}

// The backstop, which a thousand-port range means nothing reaches in practice:
// a full range is still not a reason to refuse a lease while a record in it
// has nothing listening behind it.
func TestAStaleRecordIsReclaimedWhenTheRangeRunsOut(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20000}
	shared := &recorder{ports: map[string]int{"app1\x00db": 20000}}

	r := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})
	got := acquireDetached(t, r, "app2", "db", "/src/app2")

	if got.Port != 20000 {
		t.Fatalf("port = %d, want the one port in the range", got.Port)
	}
	if _, ok := shared.ports["app1\x00db"]; ok {
		t.Error("the stale record for app1 is still there")
	}
	if port := shared.ports["app2\x00db"]; port != 20000 {
		t.Errorf("the record says app2 is on %d, not 20000", port)
	}
}

// Pressure is not licence: the last port in the range still belongs to whoever
// is listening on it, so the answer is that there is no port rather than one
// that is already serving another worktree.
func TestALiveRecordedPortIsNotReclaimedUnderPressure(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20000}
	shared := &recorder{ports: map[string]int{"app1\x00db": 20000}}
	stackStillUp := func(int) bool { return false }

	r := registry(t, lease.Options{Range: rng, Free: stackStillUp, Memory: shared})

	_, err := r.Acquire(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true})

	var exhausted *lease.ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want ExhaustedError rather than app1's port", err)
	}
	if port, ok := shared.ports["app1\x00db"]; !ok || port != 20000 {
		t.Errorf("app1's record is %d, %v; a refused lease must not disturb it", port, ok)
	}
}

// Resolve only reads, so an answer must not cost another entry its record.
func TestResolveLeavesAnotherEntrysRecordAlone(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20001}
	contested := lease.PredictPort(rng, "app2", "db")
	shared := &recorder{ports: map[string]int{"app1\x00db": contested}}

	r := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})
	if _, err := r.Resolve(lease.Request{Slug: "app2", Service: "db", Worktree: "/src/app2", Detached: true}); err != nil {
		t.Fatal(err)
	}

	if port, ok := shared.ports["app1\x00db"]; !ok || port != contested {
		t.Errorf("a reading changed the record: app1 = %d, %v", port, ok)
	}
	if _, ok := shared.ports["app2\x00db"]; ok {
		t.Error("a reading wrote a record for app2")
	}
}

// recorder is a Memory with no file behind it, so a test can watch what gets
// written without one.
type recorder struct {
	ports     map[string]int
	worktrees map[string]string
}

func (m *recorder) Port(slug, service string) (int, bool) {
	port, ok := m.ports[slug+"\x00"+service]
	return port, ok
}

func (m *recorder) Owner(port int) (string, string, bool) {
	for k, held := range m.ports {
		if held == port {
			slug, service, _ := strings.Cut(k, "\x00")
			return slug, service, true
		}
	}
	return "", "", false
}

func (m *recorder) Remember(slug, service, worktree string, port int) error {
	if m.ports == nil {
		m.ports = map[string]int{}
	}
	m.ports[slug+"\x00"+service] = port
	if m.worktrees == nil {
		m.worktrees = map[string]string{}
	}
	m.worktrees[slug] = worktree
	return nil
}

func (m *recorder) Forget(slug, service string) error {
	delete(m.ports, slug+"\x00"+service)
	return nil
}

// The value a caller needs to read a port it will not listen on.
func TestResolveAnswersWithoutTakingTheLease(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})

	found, err := r.Resolve(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 0 {
		t.Errorf("resolve took %d lease(s)", len(r.List()))
	}

	// The same port a real lease would land on, or the answer was useless.
	held := acquire(t, r, "app1", "web", "/src/app1")
	if held.Port != found.Port {
		t.Errorf("resolve promised %d, acquire gave %d", found.Port, held.Port)
	}
}

// Answering with the port something is already on is the whole point: a test
// suite reading it wants the port the dev server was handed.
func TestResolveReportsAHeldPort(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	held := acquire(t, r, "app1", "web", "/src/app1")

	found, err := r.Resolve(lease.Request{Slug: "app1", Service: "web", Worktree: "/src/app1"})
	if err != nil {
		t.Fatal(err)
	}

	if found.Port != held.Port {
		t.Errorf("resolve = %d, want the held %d", found.Port, held.Port)
	}
	// A copy, so a caller cannot end someone else's lease through it.
	found.Release()
	if len(r.List()) != 1 {
		t.Error("releasing the copy dropped the real lease")
	}
}

// Where a detached lease landed is written down so a restart puts it back. A
// reading is not a landing, so resolving must leave the file as it found it.
func TestResolveRemembersNothing(t *testing.T) {
	shared := &recorder{}
	r := registry(t, lease.Options{Free: allFree, Memory: shared})

	for _, detached := range []bool{true, false} {
		if _, err := r.Resolve(lease.Request{
			Slug: "app1", Service: "db", Worktree: "/src/app1", Detached: detached,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if len(shared.ports) != 0 {
		t.Errorf("resolve wrote %v down", shared.ports)
	}
}

// Resolve reads that file, so a detached port that moved once keeps answering
// with where it moved to rather than with the hash it no longer uses.
func TestResolveFollowsWhereADetachedPortLanded(t *testing.T) {
	rng := lease.PortRange{Low: 20000, High: 20099}
	shared := &recorder{}
	if err := shared.Remember("app1", "db", "/src/app1", 20077); err != nil {
		t.Fatal(err)
	}
	r := registry(t, lease.Options{Range: rng, Free: allFree, Memory: shared})

	found, err := r.Resolve(lease.Request{Slug: "app1", Service: "db", Worktree: "/src/app1", Detached: true})
	if err != nil {
		t.Fatal(err)
	}

	if found.Port != 20077 {
		t.Errorf("port = %d, want the remembered 20077", found.Port)
	}
}

// A slug held by another worktree is a conflict whether or not a lease is
// wanted: the port would belong to a different context.
func TestResolveRefusesAnotherWorktreesSlug(t *testing.T) {
	r := registry(t, lease.Options{Free: allFree})
	acquire(t, r, "app1", "web", "/src/app1")

	_, err := r.Resolve(lease.Request{Slug: "app1", Service: "api", Worktree: "/elsewhere/app1"})

	var collision *lease.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want a CollisionError", err)
	}
}

// The same for the one command lease names: a detached port is ended by hand,
// and the reader has to be able to run what they are told to run.
func TestBusyDetachedNamesHowGroveWasReached(t *testing.T) {
	err := (&lease.BusyError{Slug: "app1", Service: "db", Port: 20100, Detached: true}).Error()

	if !strings.Contains(err, "'"+shell.Invocation()+" release db'") {
		t.Errorf("busy error does not name the caller's grove: %q", err)
	}
}

// An entry takes the remembered-port branch every time after its first
// allocation, so a record written before paths were kept, or one whose
// worktree has since moved, only ever gains the right path here.
func TestARememberedPortStillLearnsWhereItsWorktreeIs(t *testing.T) {
	shared := &recorder{}
	if err := shared.Remember("app1", "db", "", 20040); err != nil {
		t.Fatal(err)
	}
	r := registry(t, lease.Options{Free: allFree, Memory: shared})

	got := acquireDetached(t, r, "app1", "db", "/src/app1")

	if got.Port != 20040 {
		t.Fatalf("port = %d, want the remembered 20040", got.Port)
	}
	if shared.worktrees["app1"] != "/src/app1" {
		t.Errorf("worktree = %q, want the one the lease was taken from", shared.worktrees["app1"])
	}
}

func TestAWorktreeThatMovedIsRecordedWhereItIsNow(t *testing.T) {
	shared := &recorder{}
	if err := shared.Remember("app1", "db", "/src/old", 20040); err != nil {
		t.Fatal(err)
	}
	r := registry(t, lease.Options{Free: allFree, Memory: shared})

	acquireDetached(t, r, "app1", "db", "/src/new")

	if shared.worktrees["app1"] != "/src/new" {
		t.Errorf("worktree = %q, and still names where the checkout was", shared.worktrees["app1"])
	}
}

// Resolve only reads, so asking where a port would go must not rewrite the
// path the record carries.
func TestResolveDoesNotRewriteTheRecordedWorktree(t *testing.T) {
	shared := &recorder{}
	if err := shared.Remember("app1", "db", "/src/old", 20040); err != nil {
		t.Fatal(err)
	}
	r := registry(t, lease.Options{Free: allFree, Memory: shared})

	if _, err := r.Resolve(lease.Request{Slug: "app1", Service: "db", Worktree: "/src/new", Detached: true}); err != nil {
		t.Fatal(err)
	}

	if shared.worktrees["app1"] != "/src/old" {
		t.Errorf("worktree = %q; a reading changed the record", shared.worktrees["app1"])
	}
}
