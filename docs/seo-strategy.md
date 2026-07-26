# Search and answer-engine strategy

This document is an operator checklist for the public project site. It is not
included in the GitHub Pages artifact.

## Search intent map

| Intent | Target page | Primary query family |
| --- | --- | --- |
| Product discovery | `/` | Samsung Frame TV art manager; Frame TV art sync |
| Automation | `/guides/automatically-upload-art-samsung-frame-tv.html` | automatically upload art to Samsung Frame TV; sync Frame TV artwork |
| Self-hosting | `/guides/samsung-frame-tv-docker.html` | Samsung Frame TV Docker; Frame TV art manager NAS |
| Evaluation | `/guides/frame-tv-art-manager-vs-smartthings.html` | SmartThings alternative for Frame TV art; Frame TV Manager vs SmartThings |

The pages should answer their target intent directly without repeating the
same complete article under multiple URLs.

## Post-publication indexing

1. Add the GitHub Pages property to Google Search Console.
2. Submit `https://mikeo7.github.io/frame-tv-art-manager/sitemap.xml`.
3. Inspect the home page and each guide URL, then request indexing.
4. Add the site to Bing Webmaster Tools and submit the same sitemap.
5. Confirm that Googlebot and OAI-SearchBot receive HTTP 200 responses for the
   public pages and `robots.txt`.
6. Validate the home page and guide JSON-LD with Google's Rich Results Test and
   Schema.org validator.

## Measurement loop

Review performance monthly for at least three months after initial indexing:

- Search impressions, clicks, click-through rate, and average position by page
  and query.
- Queries where a guide ranks on pages two or three; improve those pages with
  tested-model details, troubleshooting evidence, or operator examples.
- Referral traffic containing `utm_source=chatgpt.com`.
- GitHub stars, clones, container pulls, and installation issues that follow
  discovery traffic.
- Search snippets that misstate compatibility, licensing, affiliation, or
  cloud requirements.

Do not publish generic keyword articles merely to increase page count. Add a
new page only when it answers a distinct Frame TV operator question with
project-specific evidence.

## Authority and distribution

On-page work cannot guarantee first position. After the site is public:

- Add the GitHub Pages URL and concise product description to repository
  metadata.
- Publish versioned releases so installation references have durable targets.
- Share a factual launch post with relevant self-hosting and Frame TV
  communities, following each community's self-promotion rules.
- Seek inclusion in relevant self-hosted software lists only after the install
  path and support policy are stable.
- Encourage real users to document tested TV models and link to the relevant
  compatibility or troubleshooting page.

Avoid manufactured backlinks, copied comparison pages, fake reviews, and
unsupported compatibility claims.
