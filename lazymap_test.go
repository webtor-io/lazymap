package lazymap

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type TestMap struct {
	sleep time.Duration
	er    float64
	*LazyMap[int]
}

func NewTestMap(c *Config, sleep int, er float64) *TestMap {
	return &TestMap{
		sleep:   time.Duration(sleep) * time.Millisecond,
		LazyMap: New[int](c),
		er:      er,
	}
}

func (s *TestMap) Status(n int) (ItemStatus, bool) {
	return s.LazyMap.Status(fmt.Sprintf("%v", n))
}

func (s *TestMap) Get(n int) (int, error) {
	v, err := s.LazyMap.Get(fmt.Sprintf("%v", n), func() (int, error) {
		<-time.After(s.sleep)
		if rand.Float64() < s.er {
			return 0, errors.New("error")
		}
		return n, nil
	})
	if err != nil {
		return 0, nil
	}
	return v, nil
}

func (s *TestMap) Get2(n int, z int) (int, error) {
	v, err := s.LazyMap.Get(fmt.Sprintf("%v", n), func() (int, error) {
		<-time.After(s.sleep)
		if rand.Float64() < s.er {
			return 0, errors.New("error")
		}
		return z, nil
	})
	if err != nil {
		return 0, nil
	}
	return v, nil
}

func TestStatus(t *testing.T) {
	p := NewTestMap(&Config{}, 0, 0)
	p.Get(1)
	_, ok := p.Status(1)
	if !ok {
		t.Fatalf("p.Has(%v) == %v, expected true", 1, ok)
	}
}
func TestHasWithLongRunningTask(t *testing.T) {
	p := NewTestMap(&Config{
		Concurrency: 1,
	}, 1000, 0)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		p.Get(1)
		wg.Done()
	}()
	<-time.After(50 * time.Millisecond)
	go func() {
		p.Get(2)
		wg.Done()
	}()
	<-time.After(50 * time.Millisecond)
	_, ok := p.Status(1)
	if !ok {
		t.Fatalf("p.Has(%v) == %v, expected true", 1, ok)
	}
	_, ok = p.Status(2)
	if !ok {
		t.Fatalf("p.Has(%v) == %v, expected true", 2, ok)
	}
	wg.Wait()
}

// TestStatusNonBlockingDuringGet verifies that Status() doesn't block when Get() is running
func TestStatusNonBlockingDuringGet(t *testing.T) {
	lm := New[string](&Config{
		Concurrency: 2, // Allow concurrent execution
	})

	// Use a channel to signal when the function starts executing
	started := make(chan bool)

	// Start a long-running Get operation
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lm.Get("key1", func() (string, error) {
			started <- true
			time.Sleep(2 * time.Second) // Long enough to ensure we check while running
			return "value1", nil
		})
	}()

	// Wait for the function to actually start executing
	<-started

	// Give a tiny bit of time to ensure running flag is set
	time.Sleep(10 * time.Millisecond)

	// Status should not block and should complete quickly
	start := time.Now()
	status, ok := lm.Status("key1")
	elapsed := time.Since(start)

	if !ok {
		t.Fatalf("Expected Status to find key1")
	}

	if status != Running {
		t.Fatalf("Expected status Running, got %v", status)
	}

	// Status should complete in well under 100ms (it should be nearly instant)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Status() took too long (%v), suggesting it blocked on Get()", elapsed)
	}

	wg.Wait()
}

// TestStatusTransitionsDuringGet verifies Status correctly reports state transitions
func TestStatusTransitionsDuringGet(t *testing.T) {
	lm := New[int](&Config{
		Concurrency: 2,
	})

	// Check status before Get - should not exist
	status, ok := lm.Status("key1")
	if ok {
		t.Fatalf("Expected key1 to not exist, but got status %v", status)
	}
	if status != None {
		t.Fatalf("Expected status None for non-existent key, got %v", status)
	}

	// Use a channel to signal when the function starts executing
	started := make(chan bool)

	// Start a long-running Get operation
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lm.Get("key1", func() (int, error) {
			started <- true
			time.Sleep(2 * time.Second) // Long enough to ensure we check while running
			return 42, nil
		})
	}()

	// Wait for the function to actually start executing
	<-started

	// Give a tiny bit of time to ensure running flag is set
	time.Sleep(10 * time.Millisecond)

	// Check status is Running
	status, ok = lm.Status("key1")
	if !ok {
		t.Fatalf("Expected key1 to exist")
	}
	if status != Running {
		t.Fatalf("Expected status Running during Get execution, got %v", status)
	}

	// Wait for Get to complete
	wg.Wait()

	// Check status after completion - should be Done
	status, ok = lm.Status("key1")
	if !ok {
		t.Fatalf("Expected key1 to still exist after Get completes")
	}
	if status != Done {
		t.Fatalf("Expected status Done after successful Get, got %v", status)
	}
}

// TestStatusWithMultipleConcurrentGets verifies Status works correctly with multiple concurrent Gets
func TestStatusWithMultipleConcurrentGets(t *testing.T) {
	lm := New[int](&Config{
		Concurrency: 3,
	})

	var wg sync.WaitGroup
	numGoroutines := 10

	// Start multiple Get operations
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			defer wg.Done()
			lm.Get(fmt.Sprintf("key%d", n), func() (int, error) {
				time.Sleep(100 * time.Millisecond)
				return n, nil
			})
		}(i)
	}

	// Give Gets time to start
	time.Sleep(50 * time.Millisecond)

	// Check Status for all keys - should not block or deadlock
	statusCheckStart := time.Now()
	for i := 0; i < numGoroutines; i++ {
		status, ok := lm.Status(fmt.Sprintf("key%d", i))
		if !ok {
			t.Fatalf("Expected key%d to exist", i)
		}
		// Status can be Enqueued (waiting for semaphore), Running, or Done
		if status != Enqueued && status != Running && status != Done {
			t.Fatalf("Expected key%d status to be Enqueued, Running or Done, got %v", i, status)
		}
	}
	statusCheckElapsed := time.Since(statusCheckStart)

	// All Status checks should complete quickly (well under 1 second)
	// This is the key test - Status should not block even if Get is running
	if statusCheckElapsed > 500*time.Millisecond {
		t.Fatalf("Status checks took too long (%v), suggesting blocking occurred", statusCheckElapsed)
	}

	wg.Wait()

	// Verify all are Done after completion
	for i := 0; i < numGoroutines; i++ {
		status, ok := lm.Status(fmt.Sprintf("key%d", i))
		if !ok {
			t.Fatalf("Expected key%d to exist after completion", i)
		}
		if status != Done {
			t.Fatalf("Expected key%d status to be Done, got %v", i, status)
		}
	}
}

// TestStatusWithFailedGet verifies Status correctly reports Failed status
func TestStatusWithFailedGet(t *testing.T) {
	lm := New[int](&Config{
		Concurrency: 1,
		StoreErrors: true, // Store errors so we can check Failed status
	})

	// Execute a Get that will fail
	_, err := lm.Get("key1", func() (int, error) {
		return 0, errors.New("intentional error")
	})

	if err == nil {
		t.Fatalf("Expected Get to return an error")
	}

	// Check status - should be Failed
	status, ok := lm.Status("key1")
	if !ok {
		t.Fatalf("Expected key1 to exist after failed Get")
	}
	if status != Failed {
		t.Fatalf("Expected status Failed after error, got %v", status)
	}
}
func TestSequental(t *testing.T) {
	p := NewTestMap(&Config{}, 0, 0)
	for i := 0; i < 10; i++ {
		v, _ := p.Get(i)
		if v != i {
			t.Fatalf("p.Get(%v) == %v, expected %v", i, v, i)
		}
	}
	if p.Len() != 10 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 10)
	}
}
func TestExpire(t *testing.T) {
	p := NewTestMap(&Config{
		Concurrency: 1,
		Expire:      10 * time.Millisecond,
	}, 0, 0)
	v, _ := p.Get2(1, 2)
	if v != 2 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 2, v, 2)
	}
	v, _ = p.Get2(1, 5)
	if v != 2 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 2, v, 2)
	}
	<-time.After(5 * time.Millisecond)
	v, _ = p.Get2(1, 5)
	if v != 2 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 2, v, 2)
	}
	<-time.After(10 * time.Millisecond)
	v, _ = p.Get2(1, 5)
	if v != 5 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 5, v, 5)
	}
	<-time.After(5 * time.Millisecond)
	v, _ = p.Get2(1, 7)
	if v != 5 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 5, v, 5)
	}
}

func TestConcurrentWithConcurrency10(t *testing.T) {
	s := time.Now()
	p := NewTestMap(&Config{Concurrency: 10}, 300, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 10 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 10)
	}
	if time.Since(s) > time.Second {
		t.Fatalf("It takes %v seconds to perform test, expected less than a second", time.Since(s).Seconds())
	}
}

func TestConcurrentWithConcurrency10AndExpire(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 10, Expire: 10 * time.Millisecond}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	<-time.After(100 * time.Millisecond)
	if p.Len() != 0 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 0)
	}
}

func TestConcurrentWithConcurrency10AndInitExpire(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 1, InitExpire: 10 * time.Millisecond}, 200, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	<-time.After(50 * time.Millisecond)
	if p.Len() != 1 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 1)
	}
}

func TestConcurrentWithConcurrency1(t *testing.T) {
	s := time.Now()
	p := NewTestMap(&Config{
		Concurrency: 1,
	}, 300, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 10 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 10)
	}
	if time.Since(s) < 3*time.Second {
		t.Fatalf("It takes %v seconds to perform test, expected more than 3 second", time.Since(s).Seconds())
	}
}

func TestOutOfCapacity1WithConcurrency1(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 1, Capacity: 1}, 1, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 1 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 1)
	}
}

func TestOutOfCapacity1WithConcurrency1WithEviction(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 1, Capacity: 1, EvictNotInited: true}, 1, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 1 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 1)
	}
}

func TestOutOfCapacity1WithConcurrency10(t *testing.T) {
	p := NewTestMap(&Config{Capacity: 1}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() == 10 {
		t.Fatalf("len(p,m) == %v, expected less", p.Len())
	}
}

func TestOutOfCapacity100WithConcurrency10(t *testing.T) {
	p := NewTestMap(&Config{Capacity: 100}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(1000)
	for i := 0; i < 1000; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	keys := p.Keys()
	if len(keys) >= 100 {
		t.Fatalf("len(keys) == %v, expected less", len(keys))
	}
	//for _, v := range keys {
	//	j := p.m[v].val
	//	if j < 800 {
	//		t.Fatalf("j == %v, expected more than 800", j)
	//	}
	//}
}

func BenchmarkCapacity100Concurrency10(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 100}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(1000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCapacity1000Concurrency100(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 1000, Concurrency: 100}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(10000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCapacity1000WConcurrency100Expire(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 1000, Concurrency: 100, Expire: 10 * time.Millisecond, ErrorExpire: 5 * time.Millisecond}, 10, 0.1)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(10000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCapacity1000WConcurrency100ExpireAndInitExpire(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 1000, Concurrency: 100, InitExpire: 10 * time.Millisecond, Expire: 10 * time.Millisecond, ErrorExpire: 5 * time.Millisecond}, 10, 0.1)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(10000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

// Status and Touch used to read the map, and the item's fields, without any
// lock. Under -race this reproduces immediately against the unfixed code.
func TestStatusAndTouchAreRaceFree(t *testing.T) {
	m := New[int](&Config{Expire: time.Minute, Capacity: 64})
	const keys = 16
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				k := strconv.Itoa((g*7 + i) % keys)
				switch i % 3 {
				case 0:
					_, _ = m.Get(k, func() (int, error) { return i, nil })
				case 1:
					_, _ = m.Status(k)
				default:
					m.Touch(k)
				}
			}
		}(g)
	}
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// Singleflight regression: N concurrent Gets on one cold key must execute f
// exactly once, and every caller must receive its result. Broken between
// 2026-01 (f moved outside the item lock for Status visibility, with nothing
// left to make concurrent callers wait) and this test's introduction: 20
// concurrent Gets executed f 20 times.
func TestGetConcurrentSingleflight(t *testing.T) {
	m := New[int](&Config{Expire: time.Minute})
	var calls int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := m.Get("k", func() (int, error) {
				atomic.AddInt64(&calls, 1)
				time.Sleep(50 * time.Millisecond)
				return 42, nil
			})
			if err != nil {
				t.Errorf("Get: %v", err)
			}
			results[i] = v
		}(i)
	}
	close(start)
	wg.Wait()
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("f executed %d times for one key, want 1", n)
	}
	for i, v := range results {
		if v != 42 {
			t.Fatalf("caller %d got %d, want 42", i, v)
		}
	}
}

// Concurrent callers must also share a single failing execution and all see
// its error.
func TestGetConcurrentSingleflightError(t *testing.T) {
	m := New[int](&Config{Expire: time.Minute, StoreErrors: false})
	var calls int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Get("k", func() (int, error) {
				atomic.AddInt64(&calls, 1)
				time.Sleep(20 * time.Millisecond)
				return 0, errors.New("boom")
			})
			if err == nil {
				t.Error("expected the shared error, got nil")
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("f executed %d times for one key, want 1", n)
	}
}

// Capacity eviction must release the evicted item, its goroutine and its value.
//
// Before the fix, LazyMap.doExpire parked one goroutine per item on the item's
// timer channel, and Cancel() (called by clean() on capacity eviction and by
// Drop) only Stop()ped that timer — so the channel never fired, the goroutine
// never woke, and it kept the evicted item and its value reachable forever.
// TTL expiry was the only path that released anything. Measured against
// a258d93 at capacity 16 with 200 inserts: len=14, goroutines=201.
func TestEvictionReleasesGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	m := New[int](&Config{Concurrency: 1, Capacity: 16, Expire: time.Hour})
	for i := 0; i < 200; i++ {
		if _, err := m.Get(strconv.Itoa(i), func() (int, error) { return i, nil }); err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
	}
	if l := m.Len(); l > 16 {
		t.Fatalf("Len() == %v after 200 inserts at capacity 16, expected at most 16", l)
	}
	// Anything on its way out gets a moment to finish; a leak does not go away.
	limit := before + 20
	after := runtime.NumGoroutine()
	for deadline := time.Now().Add(2 * time.Second); after > limit && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	t.Logf("goroutines: before=%v after=%v len=%v", before, after, m.Len())
	if after > limit {
		t.Fatalf("goroutines: %v before, %v after 200 inserts at capacity 16 (expected at most %v) — evicted items are still pinned", before, after, limit)
	}
	runtime.KeepAlive(m)
}

// The memory half of the same bug: at capacity 16 only ~16 MiB of 1 MiB values
// may survive a forced GC. Before the fix the whole 200 MiB stayed resident.
//
// Measured as the heap the map itself retains (after minus before), not as an
// absolute figure: under -race the baseline drifts by tens of MiB between
// -count iterations, and the difference cancels that out.
func TestEvictionReleasesMemory(t *testing.T) {
	const payload = 1 << 20
	before := heapAlloc()
	m := New[[]byte](&Config{Concurrency: 1, Capacity: 16, Expire: time.Hour})
	for i := 0; i < 200; i++ {
		if _, err := m.Get(strconv.Itoa(i), func() ([]byte, error) {
			b := make([]byte, payload)
			b[0] = byte(i)
			return b, nil
		}); err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
	}
	after := heapAlloc()
	retained := int64(after) - int64(before)
	t.Logf("heap: before=%v MiB after=%v MiB retained=%v MiB len=%v",
		before>>20, after>>20, retained>>20, m.Len())
	// 16 live entries are 16 MiB; the threshold leaves 4x headroom and still
	// separates 16 MiB retained from the 200 MiB the leak retained.
	const maxRetained = 64 << 20
	if retained > maxRetained {
		t.Fatalf("map retains %v MiB after 200 inserts of 1 MiB at capacity 16, expected at most %v MiB — evicted values are still pinned",
			retained>>20, maxRetained>>20)
	}
	runtime.KeepAlive(m)
}

// heapAlloc reports live heap bytes. Two collections: the first frees, the
// second settles what the first made unreachable.
func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// Drop reaches the same Cancel() and must release as much as eviction does:
// before the fix each dropped key also left a goroutine parked on a stopped
// timer, holding the dropped item.
func TestDropReleasesGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	m := New[int](&Config{Concurrency: 1, Expire: time.Hour})
	for i := 0; i < 200; i++ {
		k := strconv.Itoa(i)
		if _, err := m.Get(k, func() (int, error) { return i, nil }); err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
		m.Drop(k)
	}
	if l := m.Len(); l != 0 {
		t.Fatalf("Len() == %v after dropping every key, expected 0", l)
	}
	limit := before + 20
	after := runtime.NumGoroutine()
	for deadline := time.Now().Add(2 * time.Second); after > limit && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	t.Logf("goroutines: before=%v after=%v", before, after)
	if after > limit {
		t.Fatalf("goroutines: %v before, %v after 200 Get+Drop (expected at most %v) — dropped items are still pinned", before, after, limit)
	}
	runtime.KeepAlive(m)
}

// An expiry timer belongs to one item, not to a key. Get's tail re-arms the
// timer after f returns, even when the item was canceled and removed while f
// ran (Drop cancels running items); that timer must never take out the fresh
// item that has since claimed the key. Before the fix the re-armed timer
// deleted by key, unconditionally.
func TestStaleTimerDoesNotDropTheFreshItem(t *testing.T) {
	m := New[int](&Config{
		Concurrency: 1,
		ErrorExpire: 50 * time.Millisecond,
		Expire:      10 * time.Second,
		StoreErrors: true,
	})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Get("k", func() (int, error) {
			<-release
			return 0, errors.New("boom")
		})
	}()
	for {
		if _, ok := m.Status("k"); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	m.Drop("k") // cancels and removes the item while f is still running
	close(release)
	<-done // Get's tail now re-arms ErrorExpire on the canceled item

	if v, err := m.Get("k", func() (int, error) { return 42, nil }); err != nil || v != 42 {
		t.Fatalf("Get after drop == (%v, %v), expected (42, nil)", v, err)
	}
	// Well past the moment the stale ErrorExpire timer would have fired, and
	// far short of the fresh item's own 10s expiry.
	time.Sleep(200 * time.Millisecond)
	if _, ok := m.Status("k"); !ok {
		t.Fatalf("the fresh item was removed by the dropped item's stale expiry timer")
	}
}

// Get's tail arms the expiry timer after f returns — including for an item that
// was canceled while f ran (Drop does that). Arming it would keep the dead item,
// and the value f just produced, alive for a whole expiry period; at Expire:
// time.Hour that is indistinguishable from the leak this fix is about.
func TestDropWhileRunningReleasesTheValue(t *testing.T) {
	const payload = 1 << 20
	const n = 50
	m := New[[]byte](&Config{Concurrency: n, Expire: time.Hour})
	before := heapAlloc()

	var started int64
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		k := strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Get(k, func() ([]byte, error) {
				atomic.AddInt64(&started, 1)
				<-release
				return make([]byte, payload), nil
			})
		}()
	}
	// Every f must be running before the drops, otherwise Get short-circuits on
	// the canceled item and never produces a value to leak.
	for atomic.LoadInt64(&started) < n {
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < n; i++ {
		m.Drop(strconv.Itoa(i))
	}
	close(release)
	wg.Wait()

	if l := m.Len(); l != 0 {
		t.Fatalf("Len() == %v after dropping every key, expected 0", l)
	}
	after := heapAlloc()
	retained := int64(after) - int64(before)
	t.Logf("heap: before=%v MiB after=%v MiB retained=%v MiB", before>>20, after>>20, retained>>20)
	const maxRetained = 16 << 20
	if retained > maxRetained {
		t.Fatalf("map retains %v MiB after dropping %v running keys of 1 MiB, expected at most %v MiB — a dead item was re-armed and pins its value",
			retained>>20, n, maxRetained>>20)
	}
	runtime.KeepAlive(m)
}

// Stop() cannot call back a callback that has already started: an expiry
// callback can fire, park on the map lock, and only get in after the item was
// evicted and another item took its key. Deleting by key alone would then take
// out the replacement, so the callback checks identity. The window is produced
// here by holding the map lock, which is the only thing the callback waits on.
func TestFiredTimerDoesNotDropTheItemThatReplacedIt(t *testing.T) {
	m := New[int](&Config{Expire: 30 * time.Millisecond})
	if _, err := m.Get("k", func() (int, error) { return 1, nil }); err != nil {
		t.Fatalf("Get: %v", err)
	}
	m.mux.Lock()
	old := m.m["k"]
	time.Sleep(100 * time.Millisecond) // old's timer fires; the callback parks here
	old.Cancel()                       // what clean()/Drop do — too late to stop it
	delete(m.m, "k")
	fresh := &lazyMapItem[int]{key: "k", val: 2, inited: true, la: time.Now()}
	m.m["k"] = fresh
	m.mux.Unlock()

	time.Sleep(100 * time.Millisecond) // the callback gets the lock and runs
	m.mux.RLock()
	got := m.m["k"]
	m.mux.RUnlock()
	if got != fresh {
		t.Fatalf("the evicted item's already-fired timer removed the item that replaced it (m[\"k\"] == %v)", got)
	}
}

// Touch must keep postponing expiry, and the item must still expire once the
// touching stops. Untested before, and the fix changed the timer underneath it:
// Reset now reschedules an AfterFunc callback instead of a channel send.
func TestTouchExtendsExpiry(t *testing.T) {
	const expire = time.Second
	m := New[int](&Config{Expire: expire})
	if _, err := m.Get("k", func() (int, error) { return 1, nil }); err != nil {
		t.Fatalf("Get: %v", err)
	}
	for i := 1; i <= 6; i++ { // 1.2s of touching against a 1s TTL, 5x headroom
		time.Sleep(200 * time.Millisecond)
		if !m.Touch("k") {
			t.Fatalf("Touch failed at %v: the item expired although it was touched every 200ms", time.Duration(i)*200*time.Millisecond)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.Status("k"); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the item never expired after touching stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Get's tail removes the item when f failed and errors are not stored. It must
// remove its own item: the item can have been dropped while f was running, and
// a fresh item can already hold the key by the time f returns. Deleting by key
// alone took that fresh item out — a spurious miss on a value that had just
// been computed. Sibling of the expiry callback's identity check.
func TestFailedGetDoesNotDropTheFreshItem(t *testing.T) {
	m := New[int](&Config{Expire: time.Hour, StoreErrors: false})
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Get("k", func() (int, error) {
			close(started)
			<-release
			return 0, errors.New("boom")
		})
	}()
	<-started
	m.Drop("k") // the failing item leaves the map while its f is still running

	var fresh int64
	if v, err := m.Get("k", func() (int, error) {
		atomic.AddInt64(&fresh, 1)
		return 42, nil
	}); err != nil || v != 42 {
		t.Fatalf("Get after drop == (%v, %v), expected (42, nil)", v, err)
	}

	close(release)
	<-done // Get's tail now removes the failed item — by identity, not by key

	if _, ok := m.Status("k"); !ok {
		t.Fatalf("the fresh item was removed by the failed Get's tail")
	}
	if v, err := m.Get("k", func() (int, error) {
		atomic.AddInt64(&fresh, 1)
		return 0, errors.New("recomputed")
	}); err != nil || v != 42 {
		t.Fatalf("Get == (%v, %v), expected the cached (42, nil)", v, err)
	}
	if n := atomic.LoadInt64(&fresh); n != 1 {
		t.Fatalf("the fresh value was computed %v times, want 1", n)
	}
}

// getRecovering calls Get the way a handler behind a recovering HTTP middleware
// does: a panic escaping Get fails the test instead of crashing the binary.
func getRecovering[T any](t *testing.T, m *LazyMap[T], key string, f func() (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic escaped Get(%q): %v", key, r)
		}
	}()
	return m.Get(key, f)
}

// A panic in f escaped Get with nothing in lazymap recovering it: the item
// stayed running with done open, and every later Get of the key blocked
// forever. The leader must get the panic as a *PanicError, and the item must go
// like any failed one, so the next Get runs f again.
//
// The stack is in Stack, never in Error(): rest-api returns err.Error() to API
// clients and torrent-store puts it in gRPC statuses.
func TestPanickingLoaderReturnsError(t *testing.T) {
	m := New[int](&Config{Expire: time.Hour})
	var calls int64
	_, err := getRecovering(t, m, "k", func() (int, error) {
		atomic.AddInt64(&calls, 1)
		panic("bad tiff")
	})
	if err == nil || !strings.Contains(err.Error(), "bad tiff") {
		t.Fatalf("Get == %v, expected an error carrying the panic value", err)
	}
	if msg := err.Error(); strings.Contains(msg, "goroutine ") || strings.Contains(msg, "lazymap_test.go") {
		t.Fatalf("Error() == %q, expected no stack", msg)
	}
	var pe *PanicError
	if !errors.As(err, &pe) || pe.Value != "bad tiff" || !strings.Contains(string(pe.Stack), "lazymap_test.go") {
		t.Fatalf("Get == %#v, expected a *PanicError with the value and a stack through the panic site", err)
	}
	if st, ok := m.Status("k"); ok {
		t.Fatalf("Status == %v after the panic, expected the failed item to be dropped", st)
	}
	if v, err := getRecovering(t, m, "k", func() (int, error) {
		atomic.AddInt64(&calls, 1)
		return 42, nil
	}); err != nil || v != 42 {
		t.Fatalf("Get after the panic == (%v, %v), expected (42, nil)", v, err)
	}
	if n := atomic.LoadInt64(&calls); n != 2 {
		t.Fatalf("f executed %v times, want 2 — a panic must not be cached when errors are not stored", n)
	}
}

// Every Get waiting on a panicking f must be released with the leader's error.
// Before, they blocked on done forever; the timeout makes that a failure rather
// than a hung test.
func TestPanickingLoaderReleasesWaiters(t *testing.T) {
	const n = 20
	before := runtime.NumGoroutine()
	m := New[int](&Config{Expire: time.Hour})
	var calls int64
	release := make(chan struct{})
	f := func() (int, error) {
		atomic.AddInt64(&calls, 1)
		<-release
		panic("bad tiff")
	}
	errs := make(chan error, n)
	get := func() {
		_, err := getRecovering(t, m, "k", f)
		errs <- err
	}
	go get()
	for st, _ := m.Status("k"); st != Running; st, _ = m.Status("k") {
		time.Sleep(time.Millisecond)
	}
	for i := 1; i < n; i++ {
		go get()
	}
	time.Sleep(100 * time.Millisecond) // the waiters park on done
	close(release)

	deadline := time.After(5 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err == nil || !strings.Contains(err.Error(), "bad tiff") {
				t.Errorf("Get == %v, expected the panic as an error", err)
			}
		case <-deadline:
			t.Fatalf("%v of %v Gets still blocked 5s after f panicked", n-i, n)
		}
	}
	if c := atomic.LoadInt64(&calls); c != 1 {
		t.Fatalf("f executed %v times, want 1 — waiters must share the leader's outcome", c)
	}
	limit := before + 5
	after := runtime.NumGoroutine()
	for deadline := time.Now().Add(2 * time.Second); after > limit && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	if after > limit {
		t.Fatalf("goroutines: %v before, %v after (expected at most %v)", before, after, limit)
	}
}

// The panic also skipped handing back the leader's concurrency slot: after
// Concurrency panics, no key — panicking or not — could load any more.
func TestPanickingLoaderReturnsConcurrencySlot(t *testing.T) {
	m := New[int](&Config{Concurrency: 1})
	_, _ = getRecovering(t, m, "a", func() (int, error) { panic("bad tiff") })
	done := make(chan error, 1)
	go func() {
		_, err := m.Get("b", func() (int, error) { return 1, nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Get(b): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Get of another key still blocked 5s after a panic at Concurrency 1 — the slot was never returned")
	}
}

// With StoreErrors the panic is kept like any returned error: Failed, not
// Running, and served without running f again until ErrorExpire.
func TestPanickingLoaderStoredAsFailure(t *testing.T) {
	m := New[int](&Config{StoreErrors: true, ErrorExpire: time.Hour})
	var calls int64
	f := func() (int, error) {
		atomic.AddInt64(&calls, 1)
		panic("bad tiff")
	}
	_, _ = getRecovering(t, m, "k", f)
	if st, ok := m.Status("k"); !ok || st != Failed {
		t.Fatalf("Status == (%v, %v) after the panic, expected (Failed, true)", st, ok)
	}
	if _, err := getRecovering(t, m, "k", f); err == nil || !strings.Contains(err.Error(), "bad tiff") {
		t.Fatalf("second Get == %v, expected the stored panic error", err)
	}
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("f executed %v times, want 1", n)
	}
}

// Under GODEBUG=panicnil=1 — the pre-1.21 semantics, still selectable at run
// time — panic(nil) recovers as nil. Keying on recover() != nil then took the
// panic for a return and cached (nil, nil) as the value.
func TestPanicNilLoaderReturnsError(t *testing.T) {
	t.Setenv("GODEBUG", "panicnil=1")
	m := New[*int](&Config{Expire: time.Hour})
	v, err := getRecovering(t, m, "k", func() (*int, error) { panic(nil) })
	if err == nil {
		t.Fatalf("Get == (%v, nil) after panic(nil), expected an error", v)
	}
	var pe *PanicError
	if !errors.As(err, &pe) || pe.Value != nil || err.Error() != "lazymap: panic in f: <nil>" {
		t.Fatalf("Get == %#v, expected a *PanicError with a nil Value", err)
	}
	if st, ok := m.Status("k"); ok {
		t.Fatalf("Status == %v after panic(nil), expected the failed item to be dropped", st)
	}
}

// A panicked error must stay reachable through errors.Is/As, as a returned one
// is: callers branch on it (context.Canceled, a domain sentinel).
func TestPanicErrorUnwrapsPanickedError(t *testing.T) {
	errBadTiff := errors.New("bad tiff")
	m := New[int](&Config{Expire: time.Hour})
	_, err := getRecovering(t, m, "k", func() (int, error) { panic(fmt.Errorf("decode: %w", errBadTiff)) })
	if !errors.Is(err, errBadTiff) {
		t.Fatalf("Get == %v, expected errors.Is to reach the panicked error", err)
	}
}

// A panic value is arbitrary — a dumped request, a multi-line message — and
// Error() lands in log lines and client responses: one line, at most 200 runes
// of value, cut on a rune boundary. Value itself is kept whole.
func TestPanicErrorMessageIsShortAndFlat(t *testing.T) {
	val := strings.Repeat("плохой tiff\r\n", 50)
	e := &PanicError{Value: val}
	msg := e.Error()
	const prefix = "lazymap: panic in f: "
	if strings.ContainsAny(msg, "\r\n") {
		t.Fatalf("Error() == %q, expected no line breaks", msg)
	}
	if !strings.HasPrefix(msg, prefix+"плохой tiff  плохой") || !utf8.ValidString(msg) ||
		utf8.RuneCountInString(msg) != utf8.RuneCountInString(prefix)+200 {
		t.Fatalf("Error() == %q, expected the prefix and the first 200 runes of the value", msg)
	}
	if e.Value != val {
		t.Fatalf("Value was modified")
	}
}
