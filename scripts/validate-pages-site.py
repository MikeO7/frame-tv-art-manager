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
ALLOWED_SUFFIXES = {".html", ".css", ".txt", ".xml", ".png", ".svg"}
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


def find_faq_questions(value: object) -> list[str]:
    found: list[str] = []
    if isinstance(value, dict):
        if value.get("@type") == "FAQPage":
            for question in value.get("mainEntity", []):
                if isinstance(question, dict) and isinstance(question.get("name"), str):
                    found.append(question["name"])
        for nested in value.values():
            found.extend(find_faq_questions(nested))
    elif isinstance(value, list):
        for nested in value:
            found.extend(find_faq_questions(nested))
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


def validate_pages(pages: dict[Path, PageParser], errors: list[str]) -> set[str]:
    indexable_urls: set[str] = set()
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
        if not parser.has_footer:
            errors.append(f"{display}: missing footer landmark")

        indexable = is_indexable(parser)
        if indexable:
            if not parser.meta.get("description"):
                errors.append(f"{display}: missing meta description")
            if len(parser.canonical) != 1:
                errors.append(f"{display}: expected one canonical URL, found {len(parser.canonical)}")
            else:
                expected = expected_canonical(path)
                if parser.canonical[0] != expected:
                    errors.append(f"{display}: canonical is {parser.canonical[0]!r}, expected {expected!r}")
                indexable_urls.add(parser.canonical[0])
            missing = sorted(key for key in REQUIRED_META if not parser.meta.get(key))
            if missing:
                errors.append(f"{display}: missing social metadata: {', '.join(missing)}")
            if path.parent.name == "guides":
                missing_heading_ids = [tag for tag, element_id in parser.heading_ids if not element_id]
                if missing_heading_ids:
                    errors.append(f"{display}: article headings missing ids: {', '.join(missing_heading_ids)}")

        if path.name == "index.html":
            visible = parser.summaries
            structured = []
            for block in parser.json_ld:
                structured.extend(find_faq_questions(block))
            if visible != structured:
                errors.append(f"{display}: visible FAQ questions do not match FAQPage JSON-LD")

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

    return indexable_urls


def validate_sitemap(indexable_urls: set[str], errors: list[str]) -> None:
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
    if set(urls) != indexable_urls:
        missing = sorted(indexable_urls - set(urls))
        extra = sorted(set(urls) - indexable_urls)
        if missing:
            errors.append(f"docs/site/sitemap.xml: missing indexable URLs: {', '.join(missing)}")
        if extra:
            errors.append(f"docs/site/sitemap.xml: URLs without indexable pages: {', '.join(extra)}")
    for entry in tree.findall("sm:url", namespace):
        if entry.find("sm:lastmod", namespace) is None:
            errors.append("docs/site/sitemap.xml: every URL requires lastmod")


def validate_llms(indexable_urls: set[str], errors: list[str]) -> None:
    text = (SITE / "llms.txt").read_text(encoding="utf-8")
    public_urls = set(re.findall(r"https://mikeo7\.github\.io/frame-tv-art-manager/[^\s)]*", text))
    unknown = sorted(url.rstrip(".,") for url in public_urls if url.rstrip(".,") not in indexable_urls)
    if unknown:
        errors.append(f"docs/site/llms.txt: unknown public URLs: {', '.join(unknown)}")


def validate_social_image(pages: dict[Path, PageParser], errors: list[str]) -> None:
    image = SITE / "assets" / "frame-tv-art-manager-social.png"
    try:
        width, height = png_dimensions(image)
    except (OSError, ValueError) as error:
        errors.append(f"{image.relative_to(ROOT)}: {error}")
        return
    if image.stat().st_size > 2_200_000:
        errors.append(f"{image.relative_to(ROOT)}: social image exceeds 2.2 MB")
    for path, parser in pages.items():
        if not is_indexable(parser):
            continue
        if parser.meta.get("og:image:width") != str(width) or parser.meta.get("og:image:height") != str(height):
            errors.append(f"{path.relative_to(ROOT)}: social image metadata does not match {width}x{height}")


def main() -> int:
    errors: list[str] = []
    validate_public_tree(errors)
    pages = parse_pages(errors)
    if not pages:
        errors.append("no public HTML pages found")
    indexable_urls = validate_pages(pages, errors)
    validate_sitemap(indexable_urls, errors)
    validate_llms(indexable_urls, errors)
    validate_social_image(pages, errors)

    if errors:
        print("Pages site validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1
    print(f"Validated {len(pages)} HTML pages and {len(indexable_urls)} sitemap URLs.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
