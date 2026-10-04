"""User settings (per machine) and catalogue folders (per topic).

Settings live in <user config dir>/sourcelens/settings.yaml (Linux:
~/.config/sourcelens/settings.yaml) and hold the default output folder and the
optional credentials. Each catalogue is a folder with its own configuration in
config/sourcelens.yaml. The default topic's catalogue is the output folder
itself; any other topic gets a subfolder named after it. The Go
implementation (cmd/sourcelens/settings.go) follows the same rules and writes
the same files.
"""

from __future__ import annotations

import datetime as dt
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from importlib import resources
from pathlib import Path

import yaml

from sourcelens.common.terms import parse_topic, term_regex, yq, ysq

SETTING_KEYS = ["output", "contact_email", "github_token", "openalex_api_key", "ncbi_api_key"]


def default_config() -> bytes:
    """The default configuration shipped with the package."""
    return resources.files("sourcelens").joinpath("defaults/default.yaml").read_bytes()


def home() -> Path:
    return Path.home()


def settings_path() -> Path:
    if os.environ.get("SOURCELENS_SETTINGS"):
        return Path(os.environ["SOURCELENS_SETTINGS"])
    if sys.platform == "win32":
        base = Path(os.environ.get("APPDATA") or home() / "AppData" / "Roaming")
    elif sys.platform == "darwin":
        base = home() / "Library" / "Application Support"
    else:
        xdg = os.environ.get("XDG_CONFIG_HOME", "")
        base = Path(xdg) if xdg and os.path.isabs(xdg) else home() / ".config"
    return base / "sourcelens" / "settings.yaml"


def load_settings() -> dict:
    try:
        data = yaml.safe_load(settings_path().read_text(encoding="utf-8")) or {}
    except (OSError, yaml.YAMLError):
        data = {}
    return {k: str(data.get(k) or "") for k in SETTING_KEYS}


def expand_home(p: str) -> str:
    if p == "~" or p.startswith("~/"):
        return str(home()) + p[1:]
    return p


def output_base(s: dict) -> Path:
    """SOURCELENS_OUTPUT, then the settings file, then ~/sourcelens."""
    p = os.environ.get("SOURCELENS_OUTPUT") or s.get("output") or str(home() / "sourcelens")
    return Path(os.path.abspath(expand_home(p)))


def apply_settings(s: dict) -> None:
    """Export credentials for the step processes."""
    def setenv(env: str, v: str) -> None:
        if not os.environ.get(env) and v:
            os.environ[env] = v
    setenv("CONTACT_EMAIL", os.environ.get("SOURCELENS_EMAIL") or s.get("contact_email", ""))
    setenv("GITHUB_TOKEN", s.get("github_token", ""))
    setenv("OPENALEX_API_KEY", s.get("openalex_api_key", ""))
    setenv("NCBI_API_KEY", s.get("ncbi_api_key", ""))
    from sourcelens.common import agelit
    agelit.set_contact(os.environ.get("CONTACT_EMAIL", ""))


def github_token_from_cli() -> None:
    """An empty GITHUB_TOKEN is taken from `gh auth token` when the GitHub CLI is logged in."""
    if os.environ.get("GITHUB_TOKEN") or not shutil.which("gh"):
        return
    try:
        out = subprocess.run(["gh", "auth", "token"], capture_output=True, text=True, timeout=20).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return
    if out:
        os.environ["GITHUB_TOKEN"] = out


# --- topics and catalogue folders ------------------------------------------------

_SLUG = re.compile(r"[^a-z0-9]+")


def topic_slug(t: str) -> str:
    s = _SLUG.sub("-", t.lower()).strip("-")
    if len(s) > 60:
        s = s[:60].rstrip("-")
    return s


def config_topic(b: bytes | str) -> str:
    try:
        data = yaml.safe_load(b) or {}
    except yaml.YAMLError:
        return ""
    return str(data.get("topic") or "") if isinstance(data, dict) else ""


def default_topic() -> str:
    return config_topic(default_config())


@dataclass
class Project:
    dir: Path
    config: Path
    topic: str = ""
    created: bool = False


class ProjectError(Exception):
    def __init__(self, msg: str, project: Project):
        super().__init__(msg)
        self.project = project


def topic_flag(t: str) -> str:
    return f' --topic "{t}"' if t else ""


def resolve_project(topic: str, output: str, create: bool, start: str = "") -> Project:
    """Find (and with create, make) the catalogue folder for a topic.

    --dir DIR               DIR
    --topic T (default)     the output folder
    --topic T (other)       <output folder>/<slug of T>
    neither                 the output folder
    """
    s = load_settings()
    apply_settings(s)
    base = output_base(s)
    if output:
        d = Path(os.path.abspath(expand_home(output)))
    elif topic and topic_slug(topic) != topic_slug(default_topic()):
        d = base / topic_slug(topic)
    else:
        d = base
    p = Project(d, d / "config" / "sourcelens.yaml")
    if p.config.is_file():
        p.topic = config_topic(p.config.read_bytes()) or default_topic()
        if topic and topic_slug(topic) != topic_slug(p.topic):
            raise ProjectError(f'{p.dir} holds the catalogue for "{p.topic}"; '
                               f'use --dir to choose another folder for "{topic}"', p)
    else:
        if not create:
            what = f'"{topic}"' if topic else "the default topic"
            raise ProjectError(f"no catalogue for {what} in {p.dir} yet; run: sourcelens update{topic_flag(topic)}", p)
        if not topic or topic_slug(topic) == topic_slug(default_topic()):
            cfg, p.topic = default_config(), default_topic()
        else:
            cfg, p.topic = topic_config(topic, start), topic
        p.config.parent.mkdir(parents=True, exist_ok=True)
        p.config.write_bytes(cfg)
        p.created = True
    from sourcelens.common import agelit
    agelit.set_project(p.dir, p.config)
    return p


def extend_start(p: Project, start: str) -> bool:
    """Move the catalogue's start_date back when a run asks for older work."""
    from sourcelens.common.agelit import query_window
    cur, _ = query_window()
    if not start or start >= str(cur):
        return False
    b = p.config.read_text(encoding="utf-8")
    rx = re.compile(r"""(?m)^(\s*start_date:\s*)["']?\d{4}-\d{2}-\d{2}["']?""")
    if not rx.search(b):
        return False
    p.config.write_text(rx.sub(lambda m: m.group(1) + '"' + start + '"', b), encoding="utf-8")
    return True


# --- configuration for a new topic -------------------------------------------------

GENERIC_CATEGORIES = r"""  category:
    - name: correction/retraction
      pub_types: 'Erratum|Retraction|Correction|Expression of Concern'
      title: '^(correction|erratum|retraction|retracted)\b|^(author )?correction to'
    - name: commentary
      pub_types: '(^|; )(Comment|Editorial|Letter|News)(;|$)'
      title: '^(comment|editorial|reply|response to|letter)\b'
    - name: review
      pub_types: 'Review|Meta-Analysis|review-article'
      title: '\breview\b|systematic review|meta-analys|\boverview\b|\bsurvey\b|state of the art|\bprimer\b|perspective|scoping|\bconsensus\b|\broadmap\b'
    - name: software/resource
      title: '\b(R|Python) package|\bpackage\b|\bsoftware\b|\btool(kit|box)?s?\b|web ?server|\bdatabase\b|\batlas\b|\bpipeline\b|\bdataset\b|\bbenchmark(ing)? (dataset|platform|suite)|\bplatform\b|\blibrary\b'
    - name: benchmark/comparison
      text: '\bbenchmark|head-to-head|systematic(ally)? (evaluat|compar)|\bcomparison of\b|\bcomparative (study|evaluation|analysis)\b'
    - name: method development
      text: '\b(we|here,? we|this (study|work|paper))\b[^.]{0,100}\b(develop|propose|present|introduce|design|construct|build|built|train)\w*\b[^.]{0,120}\b(method|model|algorithm|framework|approach|tool|technique|system|architecture|pipeline|predictor|classifier|score|index)s?\b'
    - name: intervention/trial
      text: '\brandomi[sz]ed\b|\bclinical trial\b|\bplacebo\b'
"""


def topic_config(topic: str, start: str = "") -> bytes:
    """The configuration of a new catalogue, from what the user typed."""
    terms = parse_topic(topic)
    if not start:
        t = dt.date.today()
        try:
            start = t.replace(year=t.year - 1).isoformat()
        except ValueError:  # 29 February
            start = (t.replace(day=28, year=t.year - 1) + dt.timedelta(days=1)).isoformat()
    rx = term_regex(terms)
    today = dt.date.today().isoformat()
    b = []
    b.append(f"# sourcelens configuration for the topic {yq(topic)},\n")
    b.append(f"# created {today} from: sourcelens {yq(topic)}\n")
    b.append("#\n# Edit freely: the next update reads this file again. Every key is described in\n")
    b.append("# docs/configuration.md of the sourcelens repository.\n\n")
    b.append(f"topic: {yq(topic)}\n\n")
    b.append("# Local folders mined for DOIs and links (absolute paths), e.g. your notes or a project.\n")
    b.append("references: []\n\n")
    b.append("search:\n")
    b.append(f"  start_date: {yq(start)}      # moved back automatically when a run asks for older work\n")
    b.append('  end_date: "today"\n')
    b.append("  sources: [pubmed, europepmc, arxiv, openalex]\n")
    b.append("  europepmc_sources: nonmed    # PubMed already returns MEDLINE\n")
    b.append("  max_results: 5000            # per group from OpenAlex, newest first\n")
    b.append("  groups:\n    topic:\n      scope: focused\n")
    b.append(f"      label: {yq(topic)}\n      terms:\n")
    for t in terms:
        b.append(f"        - {yq(t)}\n")
    b.append("\nclassify:\n")
    b.append("  default_category: study\n")
    b.append("  origin_category: method development\n")
    b.append("  core_categories: [method development, benchmark/comparison, review, software/resource]\n")
    b.append('  # a row is "core" when its title matches this pattern (or its category is a core category)\n')
    b.append(f"  core_title_terms: {ysq(rx)}\n")
    b.append("  landmark_roles: []\n")
    b.append(GENERIC_CATEGORIES)
    b.append("  modality: {}\n")
    b.append("  species: {}\n")
    b.append("  # named methods, models or measures to tag in the entities column; name: pattern\n")
    b.append("  entities: {}\n\n")
    b.append("repos:\n  github_queries:\n")
    for t in terms:
        b.append(f"    - {yq(t)}\n")
    b.append(f"  relevance: {ysq(rx)}\n")
    b.append(f"  package_terms: {ysq(rx)}\n")
    b.append("  known_packages: {}\n\n")
    b.append("websites:\n  # name, urls (alternatives tried in order), type, note, related_doi\n  sites: []\n")
    return "".join(b).encode("utf-8")


# --- `sourcelens config` ------------------------------------------------------------

CONFIG_HELP = """sourcelens config: show or change the settings of this machine

  sourcelens config                         settings file, output folder, credentials (masked)
  sourcelens config set KEY VALUE           change a setting
  sourcelens config unset KEY               remove a setting
  sourcelens config path                    print the settings file path

keys: output, contact_email, github_token, openalex_api_key, ncbi_api_key

Environment variables override the file: SOURCELENS_OUTPUT, SOURCELENS_EMAIL,
GITHUB_TOKEN, OPENALEX_API_KEY, NCBI_API_KEY. SOURCELENS_SETTINGS points to
another settings file.
"""


def save_settings(m: dict) -> None:
    path = settings_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    lines = ["# sourcelens settings for this machine (see: sourcelens config --help)\n"]
    lines += [f"{k}: {yq(m[k])}\n" for k in SETTING_KEYS if m.get(k)]
    path.write_text("".join(lines), encoding="utf-8")
    os.chmod(path, 0o600)


def mask(v: str) -> str:
    if not v:
        return "(not set)"
    if len(v) <= 6:
        return "set"
    return v[:4] + "*" * 8


def fail(msg: str) -> int:
    print(f"sourcelens: {msg}", file=sys.stderr)
    return 2


def cmd_config(argv: list[str], pdf_tools: str) -> int:
    if any(a in ("-h", "--help", "-help") for a in argv):
        print(CONFIG_HELP, end="")
        return 0
    s = load_settings()
    if not argv:
        print(f"{'settings file':<15} {settings_path()}")
        print(f"{'output folder':<15} {output_base(s)}")
        print(f"{'default topic':<15} {default_topic()}")
        email = os.environ.get("SOURCELENS_EMAIL") or s["contact_email"] or "(not set: Unpaywall is skipped)"
        print(f"{'contact_email':<15} {email}")
        for k in ("github_token", "openalex_api_key", "ncbi_api_key"):
            print(f"{k:<15} {mask(s[k])}")
        print(f"{'pdf conversion':<15} {pdf_tools}")
        return 0
    if argv == ["path"]:
        print(settings_path())
        return 0
    if (argv[0] == "set" and len(argv) == 3) or (argv[0] == "unset" and len(argv) == 2):
        k = argv[1]
        if k not in SETTING_KEYS:
            return fail(f'unknown setting "{k}" (one of: {", ".join(SETTING_KEYS)})')
        if argv[0] == "set":
            v = argv[2]
            if k == "output":
                v = os.path.abspath(expand_home(v))
            s[k] = v
        else:
            s.pop(k, None)
        save_settings(s)
        print(f"saved {settings_path()}")
        return 0
    return fail("usage: sourcelens config [set KEY VALUE | unset KEY | path]")


# --- `sourcelens list` ----------------------------------------------------------------

def cmd_list(argv: list[str]) -> int:
    from sourcelens.common.agelit import read_csv
    if any(a in ("-h", "--help", "-help") for a in argv):
        print("sourcelens list: the catalogues in the output folder (the default topic and one subfolder per topic)")
        return 0
    base = output_base(load_settings())
    found: list[tuple[str, Path]] = []
    cfg = base / "config" / "sourcelens.yaml"
    if cfg.is_file():
        found.append((config_topic(cfg.read_bytes()) or default_topic(), base))
    subs = []
    if base.is_dir():
        for d in sorted(base.iterdir(), key=lambda x: x.name):
            c = d / "config" / "sourcelens.yaml"
            if d.is_dir() and c.is_file():
                subs.append((config_topic(c.read_bytes()) or d.name, d))
    subs.sort(key=lambda e: e[0])
    found += subs
    if not found:
        print(f'no catalogues in {base} yet; start one with: sourcelens "your topic"')
        return 0
    for topic, d in found:
        rows = read_csv(d / "progress.csv")
        print(f"{topic[:40]:<40} {len(rows):6d} rows  {d}")
    return 0
