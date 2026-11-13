import importlib.util
import os
import subprocess
import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from unittest.mock import patch


def load_newsletter_module():
    path = Path(__file__).with_name("generate-newsletter.py")
    spec = importlib.util.spec_from_file_location("generate_newsletter", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


newsletter = load_newsletter_module()


class GetCommitsSinceTest(unittest.TestCase):
    def test_date_boundary_starts_at_midnight(self):
        with tempfile.TemporaryDirectory() as repo:
            subprocess.run(["git", "init", "-q", repo], check=True)
            subprocess.run(["git", "-C", repo, "config", "user.name", "test"], check=True)
            subprocess.run(["git", "-C", repo, "config", "user.email", "test@example.com"], check=True)
            commit_env = os.environ | {
                "GIT_AUTHOR_DATE": "2026-01-10T09:00:00+0000",
                "GIT_COMMITTER_DATE": "2026-01-10T09:00:00+0000",
            }
            subprocess.run(
                ["git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "boundary"],
                check=True,
                env=commit_env,
            )

            previous = os.getcwd()
            try:
                os.chdir(repo)
                with patch.dict(os.environ, {"TZ": "UTC", "GIT_TEST_DATE_NOW": "1768064400"}):
                    commits = newsletter.get_commits_since(datetime(2026, 1, 10))
            finally:
                os.chdir(previous)

        self.assertEqual(["boundary"], [commit["subject"] for commit in commits])


if __name__ == "__main__":
    unittest.main()
