package dto

type YouTubeSearchRequest struct {
	Query      string `json:"q"`
	MaxResults int    `json:"maxResults"`
}

type YouTubeSearchResponse struct {
	Items []YouTubeSearchItem `json:"items"`
}

type YouTubeSearchItem struct {
	VideoID string `json:"videoId"`
	Title   string `json:"title"`
	Channel string `json:"channelTitle"`
}
