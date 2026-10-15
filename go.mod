module github.com/nutanix/docker-machine

go 1.27.0

toolchain go1.27.2

require (
	github.com/google/uuid v1.6.0
	github.com/nutanix-cloud-native/prism-go-client v0.8.2
	github.com/rancher/machine v0.15.0-rancher145.2
	github.com/sirupsen/logrus v1.10.2
	go.yaml.in/yaml/v3 v3.0.5
)

require (
	github.com/Azure/go-ansiterm v0.0.0-20261013165250-51075c6ea6bc // indirect
	github.com/PaesslerAG/gval v1.2.4 // indirect
	github.com/PaesslerAG/jsonpath v0.1.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/analysis v1.0.1 // indirect
	github.com/go-openapi/errors v0.22.9 // indirect
	github.com/go-openapi/jsonpointer v1.0.2 // indirect
	github.com/go-openapi/jsonreference v1.0.3 // indirect
	github.com/go-openapi/loads v0.25.3 // indirect
	github.com/go-openapi/spec v1.0.1 // indirect
	github.com/go-openapi/strfmt v0.27.3 // indirect
	github.com/go-openapi/swag v0.29.2 // indirect
	github.com/go-openapi/swag/cmdutils v0.29.2 // indirect
	github.com/go-openapi/swag/conv v0.29.2 // indirect
	github.com/go-openapi/swag/fileutils v0.29.2 // indirect
	github.com/go-openapi/swag/jsonutils v0.29.2 // indirect
	github.com/go-openapi/swag/loading v0.29.2 // indirect
	github.com/go-openapi/swag/mangling v0.29.2 // indirect
	github.com/go-openapi/swag/netutils v0.29.2 // indirect
	github.com/go-openapi/swag/pools v0.29.2 // indirect
	github.com/go-openapi/swag/stringutils v0.29.2 // indirect
	github.com/go-openapi/swag/typeutils v0.29.2 // indirect
	github.com/go-openapi/swag/yamlutils v0.29.2 // indirect
	github.com/go-openapi/validate v1.0.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/moby/term v0.5.2 // indirect
	github.com/oklog/ulid/v2 v2.1.2 // indirect
	github.com/shopspring/decimal v1.5.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
	google.golang.org/genproto v0.0.0-20200711021454-869866162049 // indirect
	k8s.io/utils v0.0.0-20260707023825-cf1189d6abe3 // indirect
)

replace github.com/urfave/cli => github.com/urfave/cli v1.11.1-0.20151120215642-0302d3914d2a

tool golang.org/x/vuln/cmd/govulncheck

replace github.com/nutanix-cloud-native/prism-go-client => github.com/ioplane/prism-go-client v0.8.3-0.20261015001130-5af0cc292b31
