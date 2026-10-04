module github.com/v0vchansky/claude-proxy/core

go 1.24.4

require (
	github.com/amnezia-vpn/amneziawg-go v1.0.4
	golang.org/x/crypto v0.39.0
)

require (
	github.com/google/btree v1.1.3 // indirect
	github.com/tevino/abool v1.2.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/time v0.9.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250606233247-e3c4c4cad86f // indirect
)

replace gvisor.dev/gvisor => gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c

replace github.com/amnezia-vpn/amneziawg-go => ./third_party/amneziawg-go
