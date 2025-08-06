module etw_exporter

go 1.24

replace github.com/tekert/golang-etw => ../../repos/golang-etw

require (
	github.com/phuslu/log v1.0.119
	github.com/prometheus/client_golang v1.23.0
	github.com/tekert/golang-etw v0.6.0-beta1
)

require (
	github.com/0xrawsec/golang-utils v1.3.2 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.65.0 // indirect
	github.com/prometheus/procfs v0.17.0 // indirect
	golang.org/x/sys v0.34.0 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)
