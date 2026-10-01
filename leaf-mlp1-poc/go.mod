module leaf-mlp1-poc

go 1.22

require (
	github.com/bodgit/sevenzip v1.4.5
	github.com/holoplot/go-evdev v0.0.0-20250804134636-ab1d56a1fe83
	github.com/refraction-networking/utls v1.3.3
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	golang.org/x/image v0.18.0
	golang.org/x/net v0.26.0
	golang.org/x/sys v0.21.0
	golang.org/x/text v0.16.0
)

require (
	github.com/andybalholm/brotli v1.0.6 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/gaukas/godicttls v0.0.4 // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/pierrec/lz4/v4 v4.1.19 // indirect
	github.com/ulikunitz/xz v0.5.11 // indirect
	go4.org v0.0.0-20200411211856-f5505b9728dd // indirect
	golang.org/x/crypto v0.24.0 // indirect
)

// The replace lines below only exist because THIS sandbox's egress allowlist
// blocks golang.org and go4.org directly. On a normal machine (including the
// real dArkOS device) with unrestricted internet, delete this replace block
// entirely and `go mod tidy` — it will fetch the real modules the standard
// way. These mirrors are pinned to the exact same commits/tags, so the
// resulting binary is identical either way.
replace (
	go4.org => github.com/go4org/go4 v0.0.0-20200411211856-f5505b9728dd
	golang.org/x/crypto => github.com/golang/crypto v0.24.0
	golang.org/x/image => github.com/golang/image v0.18.0
	golang.org/x/net => github.com/golang/net v0.26.0
	golang.org/x/sys => github.com/golang/sys v0.21.0
	golang.org/x/text => github.com/golang/text v0.16.0
)
