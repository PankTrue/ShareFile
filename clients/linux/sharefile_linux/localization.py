"""Shared desktop translations, independent of the operating system's language."""
import json
import re
from pathlib import Path

# Installed packages include the catalog beside this module; source checkouts share it.
_catalog = Path(__file__).with_name("en.json")
if not _catalog.exists():
    _catalog = Path(__file__).resolve().parents[2] / "localization/en.json"
ENGLISH = json.loads(_catalog.read_text(encoding="utf-8"))
SOURCES = {value: key for key, value in ENGLISH.items()}
_language = "en"
_SLOT = re.compile(r"\{(\d+)(?::[^}]+)?\}")


def _template(text):
    parts, indices, start = [], [], 0
    for slot in _SLOT.finditer(text):
        parts.extend((re.escape(text[start:slot.start()]), "(.*?)"))
        indices.append(int(slot[1]))
        start = slot.end()
    return re.compile("".join(parts) + re.escape(text[start:]) + r"\Z", re.DOTALL), indices


_TEMPLATES = [(source, english, *_template(text)) for source, english in ENGLISH.items()
              if _SLOT.search(source) for text in (source, english)]


def normalize(value):
    return "ru" if value == "ru" else "en"


def set_language(value):
    global _language
    _language = normalize(value)


def language():
    return _language


def tr(key, *arguments):
    if not arguments and key not in ENGLISH and key not in SOURCES:
        for source, english, pattern, indices in _TEMPLATES:
            match = pattern.fullmatch(key)
            if match:
                values = dict(zip(indices, match.groups()))
                target = source if _language == "ru" else english
                return _SLOT.sub(lambda slot: values[int(slot[1])], target)
    source = key if key in ENGLISH else SOURCES.get(key, key)
    text = source if _language == "ru" else ENGLISH.get(source, source)
    return text.format(*arguments) if arguments else text
