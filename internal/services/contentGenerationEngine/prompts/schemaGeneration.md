Generate structured data schema markup for the following article.

Article title: {{.H1}}
Meta description: {{.MetaDescription}}
Business name: {{.BusinessName}}
URL slug: {{.URLSlug}}

FAQ section content:
{{.FAQContent}}

Generate two schema objects:
1. Article schema (type: Article)
2. FAQPage schema using the FAQ questions and answers above

Return only valid JSON. No explanation. No markdown code blocks.
Start with { and end with }.

{
  "article_schema": {
    "@context": "https://schema.org",
    "@type": "Article",
    "headline": "string",
    "description": "string",
    "author": {"@type": "Organization", "name": "string"},
    "datePublished": "string",
    "dateModified": "string"
  },
  "faq_schema": {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    "mainEntity": [
      {
        "@type": "Question",
        "name": "string",
        "acceptedAnswer": {
          "@type": "Answer",
          "text": "string"
        }
      }
    ]
  }
}