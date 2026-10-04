# Release tools for the Python package (build, twine, pytest, ruff), so that
# neither the tools nor sourcelens are installed on the machine that releases.
# Built and run by publish/publish.sh; see publish.md.

ARG PYTHON_VERSION=3.12
FROM python:${PYTHON_VERSION}-slim

ENV HOME=/tmp \
    PATH=/opt/tools/bin:$PATH \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHON_KEYRING_BACKEND=keyring.backends.null.Keyring

RUN python -m venv /opt/tools \
 && /opt/tools/bin/pip install build twine pytest ruff

WORKDIR /src
