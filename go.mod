module github.com/buildpack/pack

require (
	github.com/BurntSushi/toml v0.3.0
	github.com/Microsoft/go-winio v0.4.10
	github.com/buildpack/forge v0.0.0-20180822223310-7a787beeb5de
	github.com/buildpack/lifecycle v0.0.0-20180820122535-fa5968b9c0a6
	github.com/buildpack/packs v0.0.0-20180808181744-27a22e86e2e7
	github.com/golang/mock v1.1.1
	github.com/google/go-cmp v0.2.0
	github.com/google/go-containerregistry v0.0.0-20180731221751-697ee0b3d46e
	github.com/google/uuid v0.0.0-20171129191014-dec09d789f3d
	github.com/pkg/errors v0.8.0
	github.com/sclevine/spec v0.0.0-20180404042546-a925ac4bfbc9
	github.com/spf13/cobra v0.0.3
	github.com/spf13/pflag v1.0.2 // indirect
	golang.org/x/net v0.0.0-20180821023952-922f4815f713
	golang.org/x/sys v0.0.0-20180724212812-e072cadbbdc8
)

replace (
	github.com/docker/distribution v2.6.2+incompatible => ./vendor_docker/distribution/
	github.com/docker/docker v1.13.1 => ./vendor_docker/docker/
)
