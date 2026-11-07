#!/usr/bin/env python3
"""Validate the static GitHub Pages artifact without third-party dependencies."""

from __future__ import annotations

import json
import re
import struct
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlparse
from xml.etree import ElementTree

ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / "docs" / "site"
BASE_URL = "https://mikeo7.github.io/frame-tv-art-manager/"
PROJECT_PATH = "/frame-tv-art-manager/"
ALLOWED_SUFFIXES = {".html", ".css", ".txt", ".xml", ".png", ".svg", ".webp"}
MAX_TITLE = 60
MAX_DESCRIPTION = 155
MAX_IMAGE_BYTES = 150_000
LLMS_FILES = ("llms.txt", "llms-full.txt")
REQUIRED_META = {
    "og:type",
    "og:site_name",
    "og:title",
    "og:description",
    "og:url",
    "og:image",
    "og:image:type",
    "og:image:width",
    "og:image:height",
    "og:image:alt",
    "twitter:card",
    "twitter:title",
    "twitter:description",
    "twitter:image",
    "twitter:image:alt",
}


class PageParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.stack: list[str] = []
        self.captures: list[dict[str, object]] = []
        self.title = ""
        self.h1_text: list[str] = []
        self.summaries: list[str] = []
        self.ids: list[str] = []
        self.heading_ids: list[tuple[str, str | None]] = []
        self.links: list[tuple[str, str]] = []
        self.meta: dict[str, str] = {}
        self.canonical: list[str] = []
        self.json_ld: list[object] = []
        self.json_errors: list[str] = []
        self.has_nav = False
        self.has_footer = False
        self.has_main = False
        self.times: list[str] = []
        self.images: list[dict[str, str]] = []

    @staticmethod
    def attrs_dict(attrs: list[tuple[str, str | None]]) -> dict[str, str]:
        return {key: value or "" for key, value in attrs}

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attributes = self.attrs_dict(attrs)
        if tag in {"title", "h1", "summary", "script"}:
            if tag != "script" or attributes.get("type") == "application/ld+json":
                self.captures.append({"tag": tag, "depth": len(self.stack), "text": []})
        self.stack.append(tag)

        element_id = attributes.get("id")
        if element_id:
            self.ids.append(element_id)
        if tag in {"h2", "h3"}:
            self.heading_ids.append((tag, element_id or None))
        if tag == "nav":
            self.has_nav = True
        if tag == "footer":
            self.has_footer = True
        if tag == "main":
            self.has_main = True
        if tag == "time" and attributes.get("datetime"):
            self.times.append(attributes["datetime"])
        if tag == "img":
            self.images.append({key: value or "" for key, value in attrs if key in {"src", "alt", "width", "height"}})
        if tag == "meta":
            key = attributes.get("name") or attributes.get("property")
            if key:
                self.meta[key] = attributes.get("content", "")
        if tag == "link":
            rel = set(attributes.get("rel", "").split())
            href = attributes.get("href")
            if href:
                self.links.append(("href", href))
            if "canonical" in rel and href:
                self.canonical.append(href)
        if tag in {"a", "img", "script", "source"}:
            for attribute in ("href", "src", "srcset"):
                value = attributes.get(attribute)
                if value:
                    if attribute == "srcset":
                        for candidate in value.split(","):
                            self.links.append((attribute, candidate.strip().split()[0]))
                    else:
                        self.links.append((attribute, value))

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        self.handle_starttag(tag, attrs)
        self.handle_endtag(tag)

    def handle_data(self, data: str) -> None:
        for capture in self.captures:
            text = capture["text"]
            assert isinstance(text, list)
            text.append(data)

    def handle_endtag(self, tag: str) -> None:
        if self.stack:
            while self.stack and self.stack[-1] != tag:
                self.stack.pop()
            if self.stack:
                self.stack.pop()
        finished = [
            capture
            for capture in self.captures
            if capture["tag"] == tag and capture["depth"] == len(self.stack)
        ]
        for capture in finished:
            self.captures.remove(capture)
            text_parts = capture["text"]
            assert isinstance(text_parts, list)
            text = " ".join("".join(text_parts).split())
            if tag == "title":
                self.title = text
            elif tag == "h1":
                self.h1_text.append(text)
            elif tag == "summary":
                self.summaries.append(text)
            elif tag == "script":
                raw = "".join(text_parts).strip()
                try:
                    self.json_ld.append(json.loads(raw))
                except json.JSONDecodeError as error:
                    self.json_errors.append(str(error))


def expected_canonical(path: Path) -> str:
    relative = path.relative_to(SITE).as_posix()
    if relative == "index.html":
        return BASE_URL
    return BASE_URL + relative


def is_indexable(parser: PageParser) -> bool:
    return "noindex" not in parser.meta.get("robots", "").lower()


def local_target(source: Path, raw_url: str) -> tuple[Path | None, str]:
    if not raw_url or raw_url.startswith(("mailto:", "tel:", "data:", "javascript:")):
        return None, ""
    parsed = urlparse(raw_url)
    if parsed.scheme or parsed.netloc:
        if raw_url.startswith(BASE_URL):
            relative = unquote(parsed.path.removeprefix(PROJECT_PATH))
            target = SITE / relative
        else:
            return None, ""
    elif parsed.path.startswith(PROJECT_PATH):
        target = SITE / unquote(parsed.path.removeprefix(PROJECT_PATH))
    elif parsed.path.startswith("/"):
        return None, ""
    else:
        target = source if not parsed.path else source.parent / unquote(parsed.path)

    if parsed.path.endswith("/"):
        target = target / "index.html"
    return target.resolve(), unquote(parsed.fragment)


def png_dimensions(path: Path) -> tuple[int, int]:
    with path.open("rb") as image:
        signature = image.read(24)
    if len(signature) < 24 or signature[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("not a PNG file")
    return struct.unpack(">II", signature[16:24])


def json_ld_nodes(value: object) -> list[dict[str, object]]:
    """Flatten JSON-LD blocks and @graph arrays into a list of typed nodes."""
    found: list[dict[str, object]] = []
    if isinstance(value, dict):
        if "@type" in value:
            found.append(value)
        for nested in value.values():
            found.extend(json_ld_nodes(nested))
    elif isinstance(value, list):
        for nested in value:
            found.extend(json_ld_nodes(nested))
    return found


def nodes_of_type(parser: PageParser, *types: str) -> list[dict[str, object]]:
    return [node for block in parser.json_ld for node in json_ld_nodes(block) if node.get("@type") in types]


def find_faq_questions(parser: PageParser) -> list[str]:
    found: list[str] = []
    for faq in nodes_of_type(parser, "FAQPage"):
        for question in faq.get("mainEntity", []):  # type: ignore[union-attr]
            if isinstance(question, dict) and isinstance(question.get("name"), str):
                found.append(question["name"])
    return found


def parse_pages(errors: list[str]) -> dict[Path, PageParser]:
    pages: dict[Path, PageParser] = {}
    for path in sorted(SITE.rglob("*.html")):
        parser = PageParser()
        try:
            parser.feed(path.read_text(encoding="utf-8"))
            parser.close()
        except Exception as error:  # HTMLParser can surface malformed entity errors.
            errors.append(f"{path.relative_to(ROOT)}: HTML parse failed: {error}")
            continue
        pages[path.resolve()] = parser
    return pages


def validate_public_tree(errors: list[str]) -> None:
    if not SITE.is_dir():
        errors.append("docs/site does not exist")
        return
    for path in sorted(SITE.rglob("*")):
        if not path.is_file():
            continue
        if path.name.startswith(".") or path.suffix.lower() not in ALLOWED_SUFFIXES:
            errors.append(f"{path.relative_to(ROOT)}: unexpected public artifact file")
        elif path.suffix.lower() == ".webp" and path.stat().st_size > MAX_IMAGE_BYTES:
            errors.append(f"{path.relative_to(ROOT)}: content image exceeds {MAX_IMAGE_BYTES} bytes")


def validate_images(display: Path, parser: PageParser, errors: list[str]) -> None:
    for image in parser.images:
        source = image.get("src", "?")
        if "alt" not in image:
            errors.append(f"{display}: image {source} has no alt attribute")
        if not image.get("width") or not image.get("height"):
            errors.append(f"{display}: image {source} needs width and height to avoid layout shift")


def validate_structured_data(path: Path, display: Path, parser: PageParser, errors: list[str]) -> str | None:
    """Check page-type schema rules and return the page's dateModified, if any."""
    visible_faq = parser.summaries
    structured_faq = find_faq_questions(parser)
    if visible_faq != structured_faq:
        errors.append(f"{display}: visible FAQ questions do not match FAQPage JSON-LD")

    if path == (SITE / "index.html").resolve():
        if not structured_faq:
            errors.append(f"{display}: home page requires FAQPage JSON-LD")
        apps = nodes_of_type(parser, "SoftwareApplication")
        offers = [app.get("offers") for app in apps]
        if not apps or not all(isinstance(offer, dict) and offer.get("price") == "0" for offer in offers):
            errors.append(f"{display}: home page requires SoftwareApplication with a free Offer")
        pages = nodes_of_type(parser, "WebPage")
        return str(pages[0].get("dateModified")) if pages and pages[0].get("dateModified") else None

    if path.parent.name != "guides":
        return None
    if not nodes_of_type(parser, "BreadcrumbList"):
        errors.append(f"{display}: guide requires BreadcrumbList JSON-LD")
    articles = nodes_of_type(parser, "TechArticle", "Article")
    if len(articles) != 1:
        errors.append(f"{display}: guide requires exactly one Article or TechArticle")
        return None
    modified = articles[0].get("dateModified")
    if not isinstance(modified, str) or modified not in parser.times:
        errors.append(f"{display}: article dateModified must match a visible <time datetime>")
        return None
    return modified


def validate_metadata_lengths(display: Path, parser: PageParser, errors: list[str]) -> None:
    if len(parser.title) > MAX_TITLE:
        errors.append(f"{display}: title is {len(parser.title)} characters (max {MAX_TITLE})")
    description = parser.meta.get("description", "")
    if len(description) > MAX_DESCRIPTION:
        errors.append(f"{display}: meta description is {len(description)} characters (max {MAX_DESCRIPTION})")


def validate_links(path: Path, display: Path, parser: PageParser, pages: dict[Path, PageParser], errors: list[str]) -> None:
    for attribute, raw_url in parser.links:
        if "index.html" in raw_url and not raw_url.startswith(("http://", "https://")):
            errors.append(f"{display}: internal {attribute} promotes index.html: {raw_url}")
        target, fragment = local_target(path, raw_url)
        if target is None:
            continue
        if not target.exists():
            errors.append(f"{display}: broken local reference {raw_url!r}")
            continue
        if fragment and target.suffix.lower() == ".html":
            target_parser = pages.get(target)
            if target_parser is None or fragment not in target_parser.ids:
                errors.append(f"{display}: missing fragment #{fragment} in {target.relative_to(ROOT)}")


def validate_pages(pages: dict[Path, PageParser], errors: list[str]) -> dict[str, str | None]:
    """Validate every page and return indexable canonical URLs mapped to their dateModified."""
    indexable: dict[str, str | None] = {}
    titles: dict[str, Path] = {}
    descriptions: dict[str, Path] = {}
    for path, parser in pages.items():
        display = path.relative_to(ROOT)
        if not parser.title:
            errors.append(f"{display}: missing title")
        if len(parser.h1_text) != 1:
            errors.append(f"{display}: expected exactly one h1, found {len(parser.h1_text)}")
        duplicates = sorted({element_id for element_id in parser.ids if parser.ids.count(element_id) > 1})
        if duplicates:
            errors.append(f"{display}: duplicate ids: {', '.join(duplicates)}")
        if parser.json_errors:
            errors.append(f"{display}: invalid JSON-LD: {'; '.join(parser.json_errors)}")
        if not parser.has_nav:
            errors.append(f"{display}: missing navigation landmark")
        if not parser.has_main:
            errors.append(f"{display}: missing main landmark")
        if not parser.has_footer:
            errors.append(f"{display}: missing footer landmark")
        validate_metadata_lengths(display, parser, errors)
        validate_images(display, parser, errors)
        modified = validate_structured_data(path, display, parser, errors)

        if is_indexable(parser):
            description = parser.meta.get("description", "")
            if not description:
                errors.append(f"{display}: missing meta description")
            for value, seen, label in ((parser.title, titles, "title"), (description, descriptions, "meta description")):
                if value in seen:
                    errors.append(f"{display}: {label} duplicates {seen[value].relative_to(ROOT)}")
                seen[value] = path
            if len(parser.canonical) != 1:
                errors.append(f"{display}: expected one canonical URL, found {len(parser.canonical)}")
            else:
                expected = expected_canonical(path)
                if parser.canonical[0] != expected:
                    errors.append(f"{display}: canonical is {parser.canonical[0]!r}, expected {expected!r}")
                indexable[parser.canonical[0]] = modified
            missing = sorted(key for key in REQUIRED_META if not parser.meta.get(key))
            if missing:
                errors.append(f"{display}: missing social metadata: {', '.join(missing)}")
            if path.parent.name == "guides":
                missing_heading_ids = [tag for tag, element_id in parser.heading_ids if not element_id]
                if missing_heading_ids:
                    errors.append(f"{display}: article headings missing ids: {', '.join(missing_heading_ids)}")

        validate_links(path, display, parser, pages, errors)

    return indexable


def validate_sitemap(indexable: dict[str, str | None], errors: list[str]) -> None:
    sitemap = SITE / "sitemap.xml"
    try:
        tree = ElementTree.parse(sitemap)
    except (OSError, ElementTree.ParseError) as error:
        errors.append(f"docs/site/sitemap.xml: invalid XML: {error}")
        return
    namespace = {"sm": "http://www.sitemaps.org/schemas/sitemap/0.9"}
    urls = [element.text or "" for element in tree.findall("sm:url/sm:loc", namespace)]
    if len(urls) != len(set(urls)):
        errors.append("docs/site/sitemap.xml: duplicate URLs")
    if set(urls) != set(indexable):
        missing = sorted(set(indexable) - set(urls))
        extra = sorted(set(urls) - set(indexable))
        if missing:
            errors.append(f"docs/site/sitemap.xml: missing indexable URLs: {', '.join(missing)}")
        if extra:
            errors.append(f"docs/site/sitemap.xml: URLs without indexable pages: {', '.join(extra)}")
    for entry in tree.findall("sm:url", namespace):
        loc = entry.findtext("sm:loc", default="", namespaces=namespace)
        lastmod = entry.findtext("sm:lastmod", default="", namespaces=namespace)
        if not lastmod:
            errors.append(f"docs/site/sitemap.xml: {loc} requires lastmod")
        elif indexable.get(loc) and indexable[loc] != lastmod:
            errors.append(f"docs/site/sitemap.xml: lastmod for {loc} is {lastmod}, page dateModified is {indexable[loc]}")

    robots = (SITE / "robots.txt").read_text(encoding="utf-8")
    if f"Sitemap: {BASE_URL}sitemap.xml" not in robots:
        errors.append("docs/site/robots.txt: must reference the sitemap")


def validate_llms(indexable: dict[str, str | None], errors: list[str]) -> None:
    for name in LLMS_FILES:
        path = SITE / name
        if not path.is_file():
            errors.append(f"docs/site/{name}: missing")
            continue
        text = path.read_text(encoding="utf-8")
        public_urls = {url.rstrip(".,") for url in re.findall(r"https://mikeo7\.github\.io/frame-tv-art-manager/[^\s)|]*", text)}
        unknown = sorted(
            url for url in public_urls
            if url not in indexable and not (SITE / url.removeprefix(BASE_URL)).is_file()
        )
        if unknown:
            errors.append(f"docs/site/{name}: unknown public URLs: {', '.join(unknown)}")


def validate_social_image(pages: dict[Path, PageParser], errors: list[str]) -> None:
    for path, parser in pages.items():
        if not is_indexable(parser):
            continue
        display = path.relative_to(ROOT)
        image_url = parser.meta.get("og:image", "")
        if not image_url.startswith(BASE_URL):
            errors.append(f"{display}: og:image must be hosted on the site")
            continue
        image = SITE / image_url.removeprefix(BASE_URL)
        try:
            width, height = png_dimensions(image)
        except (OSError, ValueError) as error:
            errors.append(f"{display}: og:image {image_url}: {error}")
            continue
        if image.stat().st_size > 1_000_000:
            errors.append(f"{image.relative_to(ROOT)}: social image exceeds 1 MB")
        if (width, height) != (1200, 630):
            errors.append(f"{image.relative_to(ROOT)}: social image should be 1200x630, found {width}x{height}")
        if parser.meta.get("og:image:width") != str(width) or parser.meta.get("og:image:height") != str(height):
            errors.append(f"{display}: social image metadata does not match {width}x{height}")


def main() -> int:
    errors: list[str] = []
    validate_public_tree(errors)
    pages = parse_pages(errors)
    if not pages:
        errors.append("no public HTML pages found")
    indexable = validate_pages(pages, errors)
    validate_sitemap(indexable, errors)
    validate_llms(indexable, errors)
    validate_social_image(pages, errors)

    if errors:
        print("Pages site validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1
    print(f"Validated {len(pages)} HTML pages and {len(indexable)} sitemap URLs.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
