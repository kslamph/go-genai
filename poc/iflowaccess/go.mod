module iflowaccess

replace github.com/sunbankio/omniproxy => ../..

go 1.25.0

require (
	github.com/sashabaranov/go-openai v1.41.2
	github.com/sunbankio/omniproxy v0.0.0-00010101000000-000000000000
)

require (
	github.com/gofrs/flock v0.12.1 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/oauth2 v0.30.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
)
