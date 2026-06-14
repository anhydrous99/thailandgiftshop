package awsconfig

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
)

// Loader provides AWS SDK config to service constructors.
type Loader interface {
	Load(context.Context) (aws.Config, error)
}

// SharedLoader loads the default AWS SDK config once and reuses one HTTP
// client across every service client built from that config.
type SharedLoader struct {
	once       sync.Once
	config     aws.Config
	err        error
	httpClient *awshttp.BuildableClient
}

func NewLoader() *SharedLoader {
	return &SharedLoader{httpClient: awshttp.NewBuildableClient()}
}

func Load(ctx context.Context, loader Loader) (aws.Config, error) {
	if loader == nil {
		loader = NewLoader()
	}
	return loader.Load(ctx)
}

func (l *SharedLoader) Load(ctx context.Context) (aws.Config, error) {
	l.once.Do(func() {
		if l.httpClient == nil {
			l.httpClient = awshttp.NewBuildableClient()
		}
		l.config, l.err = config.LoadDefaultConfig(ctx, config.WithHTTPClient(l.httpClient))
	})
	return l.config, l.err
}
