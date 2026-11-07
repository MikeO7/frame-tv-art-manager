# Search and answer-engine strategy

This document is an operator checklist for the public project site. It is not
included in the GitHub Pages artifact.

## Keyword research (November 2026)

Sources: about 90 Google autocomplete queries
(`suggestqueries.google.com/complete/search?client=firefox&q=…`, including
a–z expansions of "samsung frame tv art" and "frame tv art mode"), web search
for People Also Ask style questions, and owner threads on Samsung's community
forum. Autocomplete returned nothing for self-hosting seeds (docker,
synology, unraid, self hosted, github) or for "storage full", "slideshow own
photos", and "portrait photos". Those terms have too little volume to appear,
so pages lead with mainstream pain-point wording and use the NAS and Docker
terms as secondary, long-tail targets.

Clusters seen in autocomplete, strongest fit for this project first:

1. Subscription: "samsung frame tv art without subscription", "how to get art
   on frame tv without subscription", "samsung art store subscription cost",
   "does the frame tv come with free art".
2. Free art: "free frame tv art", "frame tv art 4k free", "free public domain
   art for frame tv", "best art for frame tv free", plus seasonal variants
   (christmas, fall, halloween).
3. Uploading: "upload photos to samsung frame tv", "add photos to samsung
   frame tv iphone", "transfer photos to samsung frame tv", "add photos to
   samsung frame tv usb".
4. Size and format: "frame tv art size", "frame tv art dimensions", "samsung
   frame tv art resolution", "samsung frame tv art aspect ratio", "frame tv
   photo converter".
5. Matte and portrait: "samsung frame tv portrait art", "samsung frame tv
   vertical art", "frame tv matte border", "samsung frame tv art no mat",
   "samsung frame tv photos full screen".
6. Home Assistant: "samsung frame tv art mode home assistant", "frame tv home
   assistant".
7. Troubleshooting: "frame tv art mode not working", "samsung frame tv art
   disappeared", "frame tv slideshow not working".

Dated competitive facts used on the site, with sources cited in the pages:

- Samsung Art Store: US$4.99 a month or US$49.99 a year (SamMobile, updated
  2026-10-05). Prices vary by country.
- Owners have reported since about July 2025 on Samsung's community forum that
  the My Photos "Display All" slideshow asked for an Art Store subscription.
  Samsung's own Art Mode page still describes it as free. The site presents
  both, as owner reports, never as a confirmed Samsung policy.
- SmartThings API: Samsung announced paid access on 2026-06-23, with a
  US$4.99-a-month personal plan, and free access through Q3 2026.

Re-check these facts, and the "as of" dates on the comparison page, every
quarter.

## Keyword-to-page map

| Page | Primary keyword | Secondary keywords |
| --- | --- | --- |
| `/` | samsung frame tv art (no subscription, self-hosted) | frame tv art without subscription; samsung art store alternative; frame tv art sync; Frame TV Art Manager |
| `/guides/frame-tv-art-without-subscription.html` | frame tv art without subscription | samsung art store subscription cost; does the frame tv come with free art; frame tv slideshow own photos |
| `/guides/automatically-upload-art-samsung-frame-tv.html` | upload photos to samsung frame tv | automatically upload art; transfer photos to frame tv; upload without SmartThings; duplicate uploads |
| `/guides/samsung-frame-tv-portrait-photos-mat.html` | samsung frame tv portrait photos | vertical art; frame tv matte border; art no mat; full screen; photos cropped |
| `/guides/free-public-domain-art-samsung-frame-tv.html` | free frame tv art | free public domain art for frame tv; frame tv art 4k free; best free art; museum open access; the met; wikimedia commons |
| `/guides/seasonal-art-collections-samsung-frame-tv.html` | samsung frame tv christmas art | holiday art; seasonal art; halloween art; fall art; themed collections |
| `/guides/ai-art-samsung-frame-tv-mcp.html` | ai art for samsung frame tv | claude frame tv; chatgpt frame tv art; MCP server; generate art for frame tv |
| `/guides/samsung-frame-tv-art-size.html` | frame tv art size | 3840x2160; dimensions; resolution; aspect ratio; format jpeg; blurry |
| `/guides/samsung-frame-tv-docker.html` | samsung frame tv docker | docker compose; self-hosted frame tv art; raspberry pi |
| `/guides/frame-tv-art-synology-unraid-truenas.html` | frame tv art synology | unraid; truenas; NAS; container manager |
| `/guides/home-assistant-samsung-frame-tv-art.html` | samsung frame tv art mode home assistant | frame tv home assistant; SmartThings API fee; local control |
| `/guides/apple-photos-to-samsung-frame-tv.html` | add photos to samsung frame tv from iphone | apple photos frame tv; iPhone Shortcut; HEIC |
| `/guides/samsung-frame-tv-storage-full-art-rotation.html` | samsung frame tv storage full | how many photos can frame tv hold; delete art; art rotation; duplicates |
| `/guides/samsung-frame-tv-art-mode-troubleshooting.html` | samsung frame tv art not working | allow prompt; art not showing; art mode troubleshooting |
| `/guides/frame-tv-art-manager-vs-smartthings.html` | smartthings alternative frame tv | frame tv without smartthings; SmartThings API pricing; comparison |

The pages should answer their target intent directly without repeating the
same complete article under multiple URLs. Existing URLs are permanent; add
pages rather than renaming them.

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
  `SoftwareApplication` (with a free `Offer`) and `FAQPage`. Every guide
  carries `TechArticle` and `BreadcrumbList`. Guides with a visible
  "Questions people ask" section carry `FAQPage`, and every page's visible FAQ
  must equal its JSON-LD questions. `HowTo` appears only on the Docker,
  Synology/Unraid/TrueNAS, and AI art (MCP) guides, for the numbered steps
  shown on those pages.
- Only merged behavior is described. Planned features (none at the moment)
  are labelled "coming soon" in prose and never appear in `featureList` or
  other structured data.
- Images are WebP at 640 and 1280 pixels wide, under 150 KB each, with
  `width`, `height`, and descriptive `alt`. Before-and-after art is rendered by
  the real optimizer from public-domain works, credited on the page (artist,
  title, year, collection, public domain).
- Search is the top priority, but no trackers or third-party scripts: the
  site is static HTML and one stylesheet with system fonts.
- `llms.txt` is a concise fact sheet: defaults, limits, safety rules, a short
  common-questions list, and indexable page URLs only. `llms-full.txt` is a
  plain-text digest of every page. Update the "Last substantive update" lines
  whenever a fact changes.
- `scripts/validate-pages-site.py` enforces titles of 60 characters or fewer,
  descriptions of 155 or fewer, unique titles and descriptions, one `h1`,
  landmarks, image dimensions and alt text, JSON-LD parsing, FAQ parity,
  article dates matching the sitemap, sitemap coverage, link and fragment
  integrity, and a 1200×630 social image.
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
- "How do I get art on a Frame TV without a subscription?"
- "How do I show portrait photos on a Samsung Frame TV without cropping?"
- "Where can I get free public-domain art for The Frame?"
- "What size should pictures be for a Samsung Frame TV?"
- "How do I show Christmas art on my Frame TV only in December?"
- "Can Claude or ChatGPT make art for my Samsung Frame TV?"
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
