# litSearch, Python implementation: the image every pipeline step runs in.
#
# One image, all steps: searching, metadata resolution, full-text download and
# conversion, repository/website harvesting, and building <output>/progress.csv.
# Nothing runs on the host except python/run.sh, which starts this image.
FROM python:3.12-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# poppler-utils gives pdftotext, a fallback when PyMuPDF cannot read a PDF.
RUN apt-get update \
 && apt-get install -y --no-install-recommends poppler-utils ca-certificates \
 && rm -rf /var/lib/apt/lists/*

RUN pip install \
    "requests==2.32.*" \
    "pandas==2.2.*" \
    "pyyaml==6.0.*" \
    "lxml==5.3.*" \
    "pymupdf==1.25.*" \
    "pymupdf4llm==0.0.*" \
    "rapidfuzz==3.*" \
    "tqdm==4.*"

WORKDIR /work
