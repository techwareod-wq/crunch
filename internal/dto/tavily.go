package dto

type TavilySearchRequest struct {
	Query       string `json:"query"`
	SearchDepth string `json:"search_depth"` // "basic" or "advanced"
	MaxResults  int    `json:"max_results"`
}

type TavilySearchResponse struct {
	Results []TavilyResult `json:"results"`
}

type TavilyResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}
