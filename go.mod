module github.com/kjanat/wacogo

go 1.27.0

toolchain go1.27.4

require (
	github.com/kjanat/go-modules v0.11.1
	github.com/kjanat/wazero v1.12.2
	golang.org/x/sys v0.50.0
	golang.org/x/tools v0.53.0
)

require github.com/coreos/go-semver v0.3.1 // indirect

// v0.3.6 traps guests on body read errors. v0.3.7 also panics on valid components and forbids Content-Length.
retract [v0.3.6, v0.3.7]
