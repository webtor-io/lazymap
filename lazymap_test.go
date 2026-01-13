package lazymap

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
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
