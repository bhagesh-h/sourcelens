"""Exclusive file locks on Linux, macOS and Windows.

Linux and macOS use flock. Windows locks one byte far past the end of the
lock file (LockFileEx through msvcrt), so the file stays readable; the Go
twin (cmd/sourcelens/lock_windows.go) locks the same byte, so the two
implementations exclude each other on every platform.
"""

from __future__ import annotations

import os
import time

WINDOWS_LOCK_OFFSET = 1 << 30

if os.name == "nt":
    import msvcrt

    def _lock(fh, blocking: bool) -> bool:
        while True:
            fh.seek(WINDOWS_LOCK_OFFSET)
            try:
                msvcrt.locking(fh.fileno(), msvcrt.LK_NBLCK, 1)
                return True
            except OSError:
                if not blocking:
                    return False
                time.sleep(0.1)

    def unlock(fh) -> None:
        try:
            fh.seek(WINDOWS_LOCK_OFFSET)
            msvcrt.locking(fh.fileno(), msvcrt.LK_UNLCK, 1)
        except OSError:
            pass
else:
    import fcntl

    def _lock(fh, blocking: bool) -> bool:
        try:
            fcntl.flock(fh, fcntl.LOCK_EX if blocking else fcntl.LOCK_EX | fcntl.LOCK_NB)
            return True
        except BlockingIOError:
            return False

    def unlock(fh) -> None:
        fcntl.flock(fh, fcntl.LOCK_UN)


def lock(fh) -> None:
    """Wait for the exclusive lock of an open file."""
    _lock(fh, True)


def try_lock(fh) -> bool:
    """Take the exclusive lock of an open file if nobody holds it."""
    return _lock(fh, False)
