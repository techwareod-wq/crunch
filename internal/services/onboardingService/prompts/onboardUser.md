You are analyzing a SaaS company's website to extract structured business information for an SEO content strategist
Below is the website content:
<website_content>
{{.WebsiteContent}}
</website_content>
Extract the following information. If something is not clearly stated on the website, make a reasonable inference based on context. Do not leave fields empty — use your best judgment and mark inferred fields with "inferred": true.

If the content is an error page, a bot-check page, a parked domain, or otherwise does not contain enough information to identify the business, set "extraction_failed": true and leave the other fields as empty strings. Never invent placeholder values like "Unknown" or "Unable to determine".

Return ONLY a valid JSON object no explanation.
DO NOT wrap the JSON in markdown.
DO NOT include ```json or ``` fences.
DO NOT include explanations.
Start your response with { and end with }.
{
  "extraction_failed": boolean — true when the content does not describe an identifiable business,
  "business_name": "string — company name",
  "website": "string — domain",
  "product_type": "string — one sentence describing what the product is",
  "primary_use_case": "string — the main thing users do with this product",
  "key_features": ["string", "string", "string"] — up to 6 core features or capabilities,
  "integrations": ["string"] — tools, platforms, or services this product connects with,
  "business_model": "string — B2B SaaS / B2C / Marketplace / Agency tool / etc",
  "target_geography": "string — Global / US-focused / specific region if mentioned",
  "pricing_model": "string — subscription / freemium / usage-based / not mentioned",
  "key_differentiator": "string — what makes this product different from alternatives, in one sentence",
  "icp_signals": {
    "roles": ["string"] — job titles or roles mentioned or implied as target users,
    "industries": ["string"] — industries targeted if mentioned,
    "company_size": "string — SMB / Mid-market / Enterprise / not specified",
    "pain_points": ["string"] — problems the product claims to solve
  },
  "brand_voice_signals": "string — describe the tone of the website copy in one sentence",
  "inferred_fields": ["string"] — list any fields you inferred rather than found explicitly
}
