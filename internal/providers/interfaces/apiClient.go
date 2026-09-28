package interfaces

import "context"

type ApiClient interface {
	Get(ctx context.Context, url string, headers map[string]string) ([]byte, int, error)
	Post(ctx context.Context, url string, body any, headers map[string]string) ([]byte, int, error)
	Put(ctx context.Context, url string, body any, headers map[string]string) ([]byte, int, error)
	Delete(ctx context.Context, url string, headers map[string]string) ([]byte, int, error)
}
