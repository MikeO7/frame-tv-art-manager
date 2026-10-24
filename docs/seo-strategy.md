# Search and answer-engine strategy

This document is an operator checklist for the public project site. It is not
included in the GitHub Pages artifact.

## Search intent map

| Intent | Target page | Primary query family |
| --- | --- | --- |
| Product discovery | `/` | Samsung Frame TV art manager; Frame TV art sync |
| Automation | `/guides/automatically-upload-art-samsung-frame-tv.html` | automatically upload art to Samsung Frame TV; sync Frame TV artwork |
| Self-hosting | `/guides/samsung-frame-tv-docker.html` | Samsung Frame TV Docker; Frame TV art manager NAS, Synology, Unraid |
| Evaluation | `/guides/frame-tv-art-manager-vs-smartthings.html` | SmartThings alternative for Frame TV art; Frame TV Art Manager vs SmartThings |
| Storage full | `/guides/samsung-frame-tv-storage-full-art-rotation.html` | Samsung Frame TV storage full; rotate art on Frame TV automatically |
| Apple Photos | `/guides/apple-photos-to-samsung-frame-tv.html` | send Apple Photos to Samsung Frame TV; iPhone Shortcut Frame TV upload |

The pages should answer their target intent directly without repeating the
same complete article under multiple URLs.

Each page targets one primary query. It appears naturally in the `<title>`
(60 characters or fewer), the meta description (155 characters or fewer,
written as an answer), the `h1`, the opening paragraph, and the URL. Titles and
descriptions must stay distinct across pages.

## Answer-engine (GEO) conventions

Generative engines such as ChatGPT search, Perplexity, Google AI Overviews,
and Claude quote short, self-contained passages. The site follows these rules
so it is cited accurately:

- Every page opens with a "Short answer" paragraph, and each major section
  opens with one or two sentences that make sense when quoted alone.
- Definitional sentences use the exact entity name "Frame TV Art Manager"
  together with "Samsung Frame TV" or "The Frame".
- Concrete facts beat adjectives: 30-minute sync, 3840×2160 output, 14-day
  rotation, Tizen 8.0 or newer, port 8080, and environment-variable names.
  Each limitation and each thing the service does not do is stated plainly.
- Licensing is worded the same way everywhere: free for personal and other
  noncommercial use under PolyForm Noncommercial 1.0.0, source-available, not
  open source, and commercial use not permitted. Every page also states that
  the project is independent of Samsung.
- Guides show a visible "Last updated" date in a `<time>` element. That date
  matches `dateModified` in the JSON-LD and `lastmod` in the sitemap.
- Structured data mirrors visible content only. The homepage carries
  `SoftwareApplication` and `FAQPage`, and its visible FAQ must equal the
  JSON-LD questions (the validator enforces this). Every guide carries
  `TechArticle` and `BreadcrumbList`. `HowTo` appears only on the Docker and
  Apple Photos guides, for the numbered steps shown on those pages.
- `llms.txt` is a concise fact sheet: defaults, limits, safety rules, a short
  common-questions list, and indexable page URLs only. Update its "Last
  substantive update" line whenever a fact changes.
- `robots.txt` explicitly allows the major search and AI crawlers. GitHub
  Pages serves it under `/frame-tv-art-manager/`, but crawlers only read
  `robots.txt` at the host root (`mikeo7.github.io/robots.txt`), which this
  repository does not control. Treat the file as documentation of intent, and
  rely on the sitemap plus Search Console and Bing Webmaster Tools for
  discovery.

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

### Answer-engine checks

Once a month, ask ChatGPT (with search), Perplexity, Google (AI Overviews or
AI Mode), Microsoft Copilot, and Claude the target questions below. Record
whether the answer cites a project page and whether it gets the facts right.

- "How do I automatically upload art to a Samsung Frame TV?"
- "Samsung Frame TV storage full: how do I rotate art?"
- "Can I run Samsung Frame TV art sync in Docker on a Synology or Unraid?"
- "How do I send Apple Photos to a Samsung Frame TV?"
- "Is there a SmartThings alternative for Frame TV art?"
- "Is Frame TV Art Manager open source?" (the correct answer is no: it is
  source-available under PolyForm Noncommercial 1.0.0)

When an answer misstates licensing, affiliation, compatibility (Tizen 8.0 or
newer), or the need for a cloud account, tighten the opening answer on the
relevant page and in `llms.txt`. Several answer engines use Bing's index, so
keep Bing Webmaster Tools as current as Google Search Console.

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
