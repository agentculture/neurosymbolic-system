"""Entry point for ``python -m neurosymbolic_system``."""

from __future__ import annotations

import sys

from neurosymbolic_system.cli import main

if __name__ == "__main__":
    sys.exit(main())
