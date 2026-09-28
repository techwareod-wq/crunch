You are a visual-style analyst. The {{.ImageCount}} images above are all {{.ImageKind}} images from ONE publisher's blog articles. Derive a reusable image style prompt so newly generated {{.ImageKind}} imagery blends into this library.

Study what the images have in COMMON — palette, composition, rendering treatment, mood, subject framing, typography treatment, label/callout habits, and diagram/card layout conventions. Ignore one-off outliers. Describe the style, never the specific subjects or wording: the prompt will be reused for completely different article topics, so capture HOW text and labels look (casing, weight, color, placement, density), never WHAT they say.

Produce "image_style_prompt" (100-200 words): a block of imperative style rules for an image-generation model, phrased like the examples below (short dash-led rules, concrete and visual):

- Soft pastel palette anchored on teal and coral.
- Flat vector illustration with subtle grain texture.
- Bold navy sentence-case heading anchored top-left, with short grey supporting labels.
- Labeled pastel cards connected by thin steel-blue arrows in a left-to-right flow.

Rules for your output:
- Every rule must be observable in most of the provided images.
- Name concrete colors, textures, rendering styles (3D, flat, photo, line art), lighting, typography treatment, and composition habits.
- If the images use text, labels, callouts, or diagram/infographic layouts, capture those habits as style rules — they are part of the style.
- ALWAYS end with these exact safety rules, verbatim, as the final lines:
- Any text must be short and readable — never dense paragraphs, never gibberish filler.
- Avoid AI-slop: no clutter, no fake UI, no unreadable text, no messy infographic panels, no logos, no realistic faces.

Return only valid JSON. No explanation outside JSON. No markdown code blocks.
Start with { and end with }.

{
  "image_style_prompt": "string — the dash-led style rules, newline separated"
}
