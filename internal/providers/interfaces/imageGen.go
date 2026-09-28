package interfaces

import "context"

type ImageGenRequest struct {
	Prompt      string
	AspectRatio string // e.g. "16:9"
}

type ImageGenResponse struct {
	Data     []byte // raw image bytes returned inline by the provider
	MimeType string // e.g. "image/png"
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, req ImageGenRequest) (*ImageGenResponse, error)
}
