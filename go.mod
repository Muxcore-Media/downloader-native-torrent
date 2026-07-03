module github.com/Muxcore-Media/downloader-native-torrent

go 1.26.4

require (
	github.com/Muxcore-Media/core/pkg/contracts v0.0.0
	github.com/Muxcore-Media/core/sdk/go/module v0.1.0
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20230429144221-925a1e7659e6
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/Muxcore-Media/core v0.4.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/josharian/native v1.1.0 // indirect
	github.com/mdlayher/genetlink v1.3.2 // indirect
	github.com/mdlayher/netlink v1.7.2 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20231211153847-12269c276173 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260610212136-7ab31c22f7ad // indirect
)

replace github.com/Muxcore-Media/core => ../core

replace github.com/Muxcore-Media/core/sdk/go/module => ../core/sdk/go/module

replace github.com/Muxcore-Media/core/pkg/contracts => ../core/pkg/contracts
