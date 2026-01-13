# LazyMap Development Guidelines

## Project Overview
This is a thread-safe LazyMap implementation in Go with concurrency control, capacity management, and expiration features. The project uses Go generics (Go 1.18+) and requires Go 1.25.

## Build & Configuration

### Prerequisites
- Go 1.25 or higher
- No external dependencies (standard library only)

### Building
```bash
go build
```

### Module Information
- Module path: `github.com/webtor-io/lazymap`
- No external dependencies required

## Testing

### Running Tests

#### Run all tests:
```bash
go test -v
```

#### Run specific test:
```bash
go test -v -run TestName
```

#### Run tests with coverage:
```bash
go test -cover
go test -coverprofile=coverage.out
go tool cover -html=coverage.out
```

#### Run benchmarks:
```bash
go test -bench=.
go test -bench=BenchmarkName
```

### Test Structure

Tests are located in `lazymap_test.go` and follow standard Go testing conventions:

1. **Test Functions**: Named `TestXxx` with signature `func TestXxx(t *testing.T)`
2. **Benchmark Functions**: Named `BenchmarkXxx` with signature `func BenchmarkXxx(b *testing.B)`
3. **Helper Structs**: Use wrapper structs (e.g., `TestMap`) to simplify test setup

### Writing New Tests

#### Basic Test Pattern:
```go
func TestFeatureName(t *testing.T) {
    // Setup
    lm := New[string](&Config{
        Concurrency: 5,
    })
    
    // Execute
    value, err := lm.Get("key", func() (string, error) {
        return "value", nil
    })
    
    // Assert
    if err != nil {
        t.Fatalf("Expected no error, got: %v", err)
    }
    
    if value != "expected" {
        t.Fatalf("Expected %q, got %q", "expected", value)
    }
}
```

#### Concurrent Test Pattern:
```go
func TestConcurrentFeature(t *testing.T) {
    lm := New[int](&Config{Concurrency: 10})
    var wg sync.WaitGroup
    
    wg.Add(10)
    for i := 0; i < 10; i++ {
        go func(i int) {
            defer wg.Done()
            lm.Get(fmt.Sprintf("%d", i), func() (int, error) {
                return i, nil
            })
        }(i)
    }
    wg.Wait()
    
    // Assertions
    if lm.Len() != 10 {
        t.Fatalf("Expected 10 items, got %d", lm.Len())
    }
}
```

#### Benchmark Pattern:
```go
func BenchmarkFeature(b *testing.B) {
    lm := New[int](&Config{Capacity: 100})
    var wg sync.WaitGroup
    
    wg.Add(b.N)
    for i := 0; i < b.N; i++ {
        go func() {
            defer wg.Done()
            lm.Get("key", func() (int, error) {
                return 42, nil
            })
        }()
    }
    wg.Wait()
}
```

### Test Assertions
- Use `t.Fatalf()` for fatal errors that should stop the test immediately
- Use `t.Errorf()` for non-fatal errors to continue test execution
- Always provide descriptive error messages with actual vs expected values

### Testing Time-Based Features
- Use `time.After()` or `time.Sleep()` for delays
- Keep test timeouts reasonable (milliseconds, not seconds when possible)
- Be aware that expiration in LazyMap is passive (items expire on next access, not automatically)

## Code Style

### General Conventions
- **Indentation**: Tabs (standard Go formatting)
- **Line Length**: No strict limit, but keep lines readable
- **Formatting**: Use `gofmt` or `go fmt` before committing

### Naming Conventions
- **Types**: PascalCase (e.g., `LazyMap`, `ItemStatus`)
- **Functions/Methods**: PascalCase for exported, camelCase for unexported
- **Variables**: camelCase (e.g., `lazyMapItem`, `cleanThreshold`)
- **Constants**: PascalCase for exported enums (e.g., `Done`, `Running`)
- **Receivers**: Single letter or short abbreviation (e.g., `s` for struct methods)

### Comments
- **Package Comments**: At the top of main files
- **Exported Types/Functions**: Document with comments starting with the name
- **Implementation Details**: Comment complex logic, especially concurrency patterns
- **No Comments**: Don't over-comment obvious code

### Type Parameters (Generics)
- Use `[T any]` for generic type parameters
- Keep generic constraints simple (prefer `any` unless specific constraints needed)
- Example: `func New[T any](cfg *Config) *LazyMap[T]`

### Struct Design
- **Embedding**: Use `noCopy` struct to prevent accidental copying
- **Mutex Placement**: Place `sync.RWMutex` or `sync.Mutex` as struct fields
- **Field Ordering**: Group related fields together
- **Unexported Fields**: Keep internal state unexported

### Concurrency Patterns
- **Double-Check Locking**: Used in `Get()` method (RLock → check → RUnlock → Lock → check again)
- **Channel-Based Semaphores**: Use buffered channels for concurrency control (see `c chan bool`)
- **Mutex Granularity**: Use `RWMutex` for read-heavy operations, regular `Mutex` for items
- **Defer Unlocks**: Always defer mutex unlocks immediately after locking

### Error Handling
- Return errors as second return value: `(T, error)`
- Create custom error types when needed (e.g., `EvictedError`)
- Don't panic in library code; return errors instead

### Configuration Pattern
- Use a `Config` struct for initialization options
- Provide sensible defaults in the constructor
- Allow zero values to mean "use default"
- Example:
  ```go
  concurrency := 10
  if cfg.Concurrency != 0 {
      concurrency = cfg.Concurrency
  }
  ```

### Method Receivers
- Use pointer receivers for:
  - Methods that modify the receiver
  - Large structs
  - Consistency (if any method uses pointer receiver, all should)
- Receiver name: Use short, consistent names (e.g., `s` for struct)

## Key Implementation Details

### LazyMap Core Features
1. **Lazy Evaluation**: Values are computed on-demand via provided function
2. **Concurrency Control**: Semaphore pattern using buffered channel
3. **Capacity Management**: LRU-based eviction when capacity threshold reached
4. **Expiration**: Time-based expiration for both successful and error results
5. **Thread Safety**: All operations are thread-safe using RWMutex

### Important Types
- `LazyMap[T]`: Main map structure with generic type parameter
- `lazyMapItem[T]`: Internal item wrapper with state management
- `ItemStatus`: Enum for item lifecycle (None, Enqueued, Running, Done, Failed, Canceled)
- `Config`: Configuration struct for initialization

### Critical Methods
- `Get(key, func)`: Main method - retrieves or computes value
- `Status(key)`: Check item status without triggering computation
- `Touch(key)`: Update last access time (for LRU)
- `Drop(key)`: Manually remove item
- `clean()`: Internal LRU eviction logic

## Common Pitfalls

1. **Copying LazyMap**: Never copy LazyMap instances (use pointers). The `noCopy` struct helps `go vet` detect this.

2. **Expiration Behavior**: Expiration is passive - items don't auto-delete, they expire on next access or during cleanup.

3. **Concurrency Limits**: The `Concurrency` config limits parallel function executions, not map access.

4. **Capacity vs Length**: Capacity triggers cleanup, but `Len()` may temporarily exceed capacity during concurrent operations.

5. **Error Storage**: By default, errors are not stored. Set `StoreErrors: true` to cache error results.

## Development Workflow

1. Make changes to code
2. Run `go fmt` to format code
3. Run `go vet` to check for common mistakes
4. Run tests: `go test -v`
5. Run benchmarks if performance-critical: `go test -bench=.`
6. Check test coverage: `go test -cover`

## Performance Considerations

- Use appropriate `Concurrency` settings based on workload
- Set `Capacity` to prevent unbounded memory growth
- Use `Expire` and `ErrorExpire` to prevent stale data
- Consider `InitExpire` for slow initialization functions
- Benchmark with realistic workloads using the benchmark tests
