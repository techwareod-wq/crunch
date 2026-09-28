package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	transcriptapi "github.com/atharva-ng/ytTranscriptApi"
)

var _ interfaces.YouTube = (*youtubeProvider)(nil)

var (
	instance *youtubeProvider
	once     sync.Once
)

type youtubeProvider struct {
	apiClient     interfaces.ApiClient
	apiKey        string
	searchURL     string
	transcriptAPI *transcriptapi.API
}

func GetProvider(apiClient interfaces.ApiClient, apiKey string, webshareUsername, websharePassword, searchURL string) interfaces.YouTube {
	once.Do(func() {
		api := &transcriptapi.API{}
		if webshareUsername != "" && websharePassword != "" {
			api.Proxy = &transcriptapi.WebshareProxyConfig{
				Username: webshareUsername,
				Password: websharePassword,
			}
		}
		instance = &youtubeProvider{
			apiClient:     apiClient,
			apiKey:        apiKey,
			searchURL:     searchURL,
			transcriptAPI: api,
		}
	})
	return instance
}

type youtubeAPIResponse struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string `json:"title"`
			ChannelTitle string `json:"channelTitle"`
		} `json:"snippet"`
	} `json:"items"`
}

func (y *youtubeProvider) SearchVideos(ctx context.Context, req dto.YouTubeSearchRequest) (*dto.YouTubeSearchResponse, error) {
	params := url.Values{
		"part":          {"snippet"},
		"q":             {req.Query},
		"type":          {"video"},
		"maxResults":    {strconv.Itoa(req.MaxResults)},
		"order":         {"relevance"},
		"videoDuration": {"medium"},
		"key":           {y.apiKey},
	}

	fullURL := y.searchURL + "?" + params.Encode()
	headers := map[string]string{}

	respBody, statusCode, err := y.apiClient.Get(ctx, fullURL, headers)
	if err != nil {
		return nil, fmt.Errorf("youtube: request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube: unexpected status code %d: %s", statusCode, string(respBody))
	}

	var apiResp youtubeAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("youtube: failed to unmarshal response: %w", err)
	}

	var items []dto.YouTubeSearchItem
	for _, item := range apiResp.Items {
		items = append(items, dto.YouTubeSearchItem{
			VideoID: item.ID.VideoID,
			Title:   item.Snippet.Title,
			Channel: item.Snippet.ChannelTitle,
		})
	}

	return &dto.YouTubeSearchResponse{Items: items}, nil
}

func (y *youtubeProvider) GetTranscript(ctx context.Context, videoID string) (string, error) {
	list, err := y.transcriptAPI.List(ctx, videoID)
	if err != nil {
		return "", fmt.Errorf("youtube: transcript: %w", err)
	}

	t := findEnglishTranscript(list)
	if t == nil {
		// No English track — try translating the first available track.
		if len(list.All) == 0 {
			return "", fmt.Errorf("youtube: transcript: no tracks available for %s", videoID)
		}
		translated, err := list.All[0].Translate("en")
		if err != nil {
			return "", fmt.Errorf("youtube: transcript: %w", err)
		}
		t = translated
	}

	fetched, err := t.Fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("youtube: transcript: %w", err)
	}

	var parts []string
	for _, seg := range fetched.Snippets {
		text := strings.TrimSpace(seg.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, " "), nil
}

// findEnglishTranscript does prefix matching on language codes (e.g. "en", "en-US", "en-GB").
// Manual transcripts are preferred over generated.
func findEnglishTranscript(list *transcriptapi.TranscriptList) *transcriptapi.Transcript {
	for code, t := range list.Manual {
		if strings.HasPrefix(code, "en") {
			return t
		}
	}
	for code, t := range list.Generated {
		if strings.HasPrefix(code, "en") {
			return t
		}
	}
	return nil
}
